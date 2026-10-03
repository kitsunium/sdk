// Package kit — the Studio's read API: the graph, the events, the sources.
package kit

import (
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/health"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// studioCSP is the content security policy of the Studio. It shares the
// product's origin and shows attacker-controllable data — titles, errors,
// identifiers — so it runs nothing that is not its own bundle.
const studioCSP = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

// studioPackage is the package a product imports to serve the Studio's API.
const studioPackage = "github.com/kitsunium/sdk/framework/kit/studio"

// Limits of the source endpoint.
const (
	sourceContext  int = 12
	sourceMaxLines int = 400
	sourceMaxBytes     = 2 << 20
)

var (
	// sourceLanguages are the files the source endpoint serves, by extension.
	sourceLanguages = map[string]string{
		".go": "go", ".js": "javascript", ".mjs": "javascript", ".ts": "typescript",
		".html": "xml", ".css": "css", ".json": "json",
	}

	// errNotSource is readSource's refusal: the name holds no regular file
	// within sourceMaxBytes, or no longer holds the one it looked at.
	errNotSource = errs.New(CodeNotSource, "NOT_SOURCE", "not a regular source file", "kit: the source endpoint refused a name that holds no regular file")

	// studioMount mounts the Studio's API — mountIntrospection — once
	// framework/kit/studio is imported, and is nil before: a product that does
	// not import it links none of the Studio's routes, its event stream, the dev
	// tools or the profiler.
	studioMount atomic.Pointer[func(a *App, mux *http.ServeMux)]
)

// mountHealth serves the three probes, always — production included, and
// whatever the Host header says: an orchestrator probes by IP. It also
// reserves the rest of /_kit/: without the Studio, a path there answers 404
// rather than falling through to a frontend's index.html.
func (a *App) mountHealth(mux *http.ServeMux) {
	cfg := health.HandlerConfig{Detail: a.cfg.env == EnvDev}
	mux.Handle("GET /_kit/health/live", health.NewLivenessHandler(a.health, cfg))
	mux.Handle("GET /_kit/health/ready", health.NewReadinessHandler(a.health, cfg))
	mux.Handle("GET /_kit/health/startup", health.NewStartupHandler(a.health, cfg))
	if !a.cfg.studio.on {
		// Every method kit routes answers 404: a write to a dev tool's route
		// is refused like a read, not with a 405 that says it exists.
		notFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a.replyError(r.Context(), w, NotFound("the Studio is only served in dev (KIT_ENV=dev)"))
		})
		for _, method := range httpMethods {
			if method != http.MethodHead {
				mux.Handle(method+" /_kit/", notFound)
			}
		}
	}
}

// EnableStudio makes a serving app in dev mount the Studio's API.
// framework/kit/studio calls it as it is imported; a product never does.
func EnableStudio() {
	mount := (*App).mountIntrospection
	studioMount.Store(&mount)
}

// mountIntrospection serves, in dev, what the Studio reads about the
// running product — the graph, live events, traces, source, instances, items
// and the dev tools' reads. The Studio itself — its pages, its assets — is
// the kit tool's (ADR 0147 §1), which reads these routes; the framework
// links no user interface. Every route checks the Host header against an
// allow-list, so a page on another site cannot read the product's source or
// live traffic through DNS rebinding. None of them acts on the product (D13).
func (a *App) mountIntrospection(mux *http.ServeMux) {
	api := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, a.hostGuard(h))
	}
	api("GET /_kit/api/graph", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, a.Graph())
	})
	api("GET /_kit/api/events", a.serveEvents)
	api("GET /_kit/api/traces", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		writeJSON(w, http.StatusOK, a.hub.recentTraces(100, q.Get("root"), q.Get("node")))
	})
	api("GET /_kit/api/traces/{id}", a.serveTrace)
	api("GET /_kit/api/source", a.serveSource)
	api("GET /_kit/api/instances", a.serveInstances)
	api("GET /_kit/api/items", a.serveItems)
	a.mountDevTools(api)
	a.mountNotFound(api, "/_kit/api/", "no such Studio API")
	a.mountNotFound(api, "/_kit/", "the Studio is the kit tool's, not the product's: run kit dev")
}

// mountNotFound answers every method but HEAD under prefix with a not found
// that says message.
func (a *App) mountNotFound(api func(pattern string, h http.HandlerFunc), prefix, message string) {
	for _, method := range httpMethods {
		if method == http.MethodHead {
			continue
		}
		api(method+" "+prefix, func(w http.ResponseWriter, r *http.Request) {
			a.replyError(r.Context(), w, NotFound(message))
		})
	}
}

// serveTrace answers GET /_kit/api/traces/{id}: one trace, while the hub
// keeps it.
func (a *App) serveTrace(w http.ResponseWriter, r *http.Request) {
	t, ok := a.hub.trace(r.PathValue("id"))
	if !ok {
		a.replyError(r.Context(), w, NotFound("no such trace, or it was evicted"))
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// serveInstances answers GET /_kit/api/instances?workflow=: a workflow's
// entities and where each is.
func (a *App) serveInstances(w http.ResponseWriter, r *http.Request) {
	wf, ok := a.findNode(r.URL.Query().Get("workflow")).(interface{ instanceList() []model.Instance })
	if !ok {
		a.replyError(r.Context(), w, NotFound("no such workflow"))
		return
	}
	writeJSON(w, http.StatusOK, wf.instanceList())
}

// serveItems answers GET /_kit/api/items?store=: a store's latest entities.
// They go through the same redaction as payloads: a store of accounts holds
// password hashes and token digests, and personal data (ADR 0006), which the
// Studio has no reason to show.
func (a *App) serveItems(w http.ResponseWriter, r *http.Request) {
	st, ok := a.findNode(r.URL.Query().Get("store")).(itemsSource)
	if !ok {
		a.replyError(r.Context(), w, NotFound("no such store"))
		return
	}
	items := st.items(200)
	out := make([]json.RawMessage, len(items))
	for i, raw := range items {
		out[i] = st.redacted(raw)
	}
	writeJSON(w, http.StatusOK, out)
}

// hostGuard refuses a request that does not come from this machine, or whose
// Host is not an allowed name. The Host check stops DNS rebinding — a page on
// another site reaching a local port through a name it controls; the address
// check stops everyone else on the network, whatever Host they send.
func (a *App) hostGuard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.cfg.studio.remote && !loopback(r.RemoteAddr) {
			writeJSON(w, http.StatusForbidden, wireError{Error: wireBody{
				Code: WireForbidden, Message: "the Studio answers this machine only; set KIT_STUDIO_REMOTE=on to reach it through a forwarded port",
			}})
			return
		}
		host := r.Host
		if hh, _, err := net.SplitHostPort(host); err == nil {
			host = hh
		}
		host = strings.ToLower(strings.Trim(host, "[]"))
		if !slices.Contains(a.cfg.allowedHosts, host) {
			writeJSON(w, http.StatusForbidden, wireError{Error: wireBody{
				Code: WireForbidden, Message: "this host may not reach the Studio; add it to KIT_ALLOWED_HOSTS",
			}})
			return
		}
		w.Header().Set("Content-Security-Policy", studioCSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
}

// loopback reports whether a remote address is this machine.
func loopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// findNode returns the building block with the given ID, or nil.
func (a *App) findNode(id string) node {
	for _, svc := range a.services {
		if svc == nil || svc.name != model.ServiceOf(id) {
			continue
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if n.base().id == id {
				return n
			}
		}
	}
	return nil
}

// serveEvents streams live events as Server-Sent Events. The stream opens
// with a hello carrying the graph revision, so a Studio that reconnects after
// a reload knows whether to fetch the graph again.
//
// The stream itself — framing, the keep-alive comment a proxy needs to see,
// the bound on each frame's write, the end on disconnect — is the SDK's
// net/sse; the Studio's own is only which events, in which order.
func (a *App) serveEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	open := plug.OpenEventStream.Load()
	//: a product that did not import framework/kit/studio has no stream to
	//: open: the route answers as the Studio's absence, never a 500.
	if open == nil {
		writeJSON(w, http.StatusNotFound, wireError{Error: wireBody{Code: WireNotFound, Message: "the Studio is not linked: import framework/kit/studio"}})
		return
	}
	stream, err := (*open)(w, r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, wireError{Error: wireBody{Code: WireInternal, Message: "internal error"}})
		return
	}
	defer func() {
		if err := stream.Close(); err != nil {
			logger.Debug(r.Context(), a.log, "the Studio's event stream did not close cleanly", logger.String("error", err.Error()))
		}
	}()
	events, cancel := a.hub.subscribe()
	defer cancel()
	// An event that does not encode is skipped; one that cannot be written
	// ends the stream.
	send := func(e model.Event) error {
		raw, err := json.Marshal(e)
		if err != nil {
			return nil
		}
		return stream.Send(string(raw))
	}
	g := a.Graph()
	if send(model.Event{Type: model.EventHello, Time: a.clock.Now().UTC(), Revision: g.Revision, StartedAt: g.App.StartedAt, Phase: a.currentPhase()}) != nil {
		return
	}
	for {
		select {
		case <-stream.Done():
			return
		case <-a.streams.Done():
			return
		case e := <-events:
			if send(e) != nil {
				return
			}
		}
	}
}

// serveSource returns lines of a source file around a range. It serves only
// files the graph itself points at — never a path the request makes up — and
// reads them through an os.Root confined to the file's root: the product's
// module, or, with module=, another Go module of the build whose root a
// position revealed — a mounted module's, a library's. A regular file only,
// never a link at its name, even one put there while it is read
// (readSource); a link on its path is followed inside the root only, as
// os.Root does. So no request reaches .env, .git or the data directory.
func (a *App) serveSource(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	file, module := q.Get("file"), q.Get("module")
	notFound := func() { a.replyError(r.Context(), w, NotFound("no such source in this product's graph")) }
	if file == "" || !slices.Contains(a.Graph().Files(), model.File{GoModule: module, File: file}) {
		notFound()
		return
	}
	lang, ok := sourceLanguages[path.Ext(file)]
	if !ok || !filepath.IsLocal(filepath.FromSlash(file)) {
		notFound()
		return
	}
	root, err := a.openSourceRoot(module)
	if err != nil {
		notFound()
		return
	}
	defer func() {
		if err := root.Close(); err != nil {
			logger.Debug(r.Context(), a.log, "the source root did not close cleanly", logger.String("error", err.Error()))
		}
	}()
	raw, err := readSource(root, filepath.FromSlash(file))
	if err != nil {
		notFound()
		return
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	line := atoiOr(q.Get("line"), 1)
	end := atoiOr(q.Get("end"), line)
	line = min(max(line, 1), len(lines))
	end = min(max(end, line), len(lines), line+sourceMaxLines)
	first := max(1, line-sourceContext)
	last := min(len(lines), end+sourceContext)
	writeJSON(w, http.StatusOK, model.Snippet{
		File: file, StartLine: first, Focus: line, FocusEnd: end, Lines: lines[first-1 : last], Language: lang,
	})
}

// openSourceRoot opens the directory the files of a root lie in: the
// product's module for "", another Go module — a mounted module's, a
// library's — as its positions revealed it.
func (a *App) openSourceRoot(module string) (*os.Root, error) {
	dir, known := a.root, a.root != ""
	if module != "" {
		dir, known = a.roots.get(module)
	}
	if !known {
		return nil, os.ErrNotExist
	}
	return os.OpenRoot(dir)
}

// readSource reads the file name of root: a regular file of at most
// sourceMaxBytes, never a link. It looks at the name without following it —
// a link, a directory or a named pipe there is refused unopened —, then
// reads the file it saw (readSeen).
func readSource(root sourceRoot, name string) ([]byte, error) {
	seen, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !seen.Mode().IsRegular() {
		return nil, errNotSource
	}
	return readSeen(root, name, seen)
}

// readSeen opens name once, with sourceOpen's flags, and reads from that
// handle only when the handle is the file seen — os.SameFile: the same
// device and inode, or on Windows the same volume and file index —, a
// regular file within the bound. Whatever was put at the name since it was
// seen is refused: a link is never read through, whether the open follows
// it, as os.Root does on Unix, or opens the link itself, as on Windows. It
// reads no more than the bound, even from a file that grew since its Stat.
func readSeen(root openFiler, name string, seen fs.FileInfo) (content []byte, err error) {
	f, err := root.OpenFile(name, sourceOpen, 0)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			content, err = nil, cerr
		}
	}()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(seen, info) || info.Size() > sourceMaxBytes {
		return nil, errNotSource
	}
	content, err = io.ReadAll(io.LimitReader(f, sourceMaxBytes+1))
	if err == nil && len(content) > sourceMaxBytes {
		return nil, errNotSource
	}
	return content, err
}

// atoiOr is text as a decimal integer, or fallback when it is not one: a
// query parameter the Studio may leave out.
func atoiOr(text string, fallback int) int {
	n, err := strconv.Atoi(text)
	if err != nil {
		return fallback
	}
	return n
}
