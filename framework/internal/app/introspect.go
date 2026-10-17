package app

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"sync/atomic"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/health"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// studioPackage is the package a product imports to serve the Studio's API.
const studioPackage = "github.com/kitsunium/sdk/framework/app/studio"

// sourceMaxBytes bounds a file the source endpoint reads.
const sourceMaxBytes = 2 << 20

var (
	// errNotSource is readSource's refusal: the name holds no regular file
	// within sourceMaxBytes, or no longer holds the one it looked at.
	errNotSource = errs.New(CodeNotSource, "NOT_SOURCE", "not a regular source file", "kit: the source endpoint refused a name that holds no regular file")

	// studioMount mounts the Studio's routes — framework/app/studio's, over a
	// host of the app (studio_host.go) — once framework/app/studio is
	// imported, and is nil before: a product that does not import it links
	// none of the Studio's routes, its event stream, the dev tools, the
	// profiler, nor what the core reads for them (ADR 0180 step 1).
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

// EnableStudio makes a serving app in dev mount the Studio's routes, which
// framework/app/studio plugs in (plug.Studio), over a host of the app.
// framework/app/studio calls it as it is imported; a product never does.
func EnableStudio() {
	mount := mountStudio
	studioMount.Store(&mount)
}

// mountStudio mounts the Studio's routes on mux, over a host of a: the one
// place a studioHost is made, which only EnableStudio reaches.
func mountStudio(a *App, mux *http.ServeMux) {
	if d := plug.Studio.Load(); d != nil {
		d.Mount(mux, studioHost{a: a})
	}
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
