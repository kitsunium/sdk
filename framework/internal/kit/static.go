// Package kit — frontends: static assets the product serves.
package kit

import (
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// frontendCSP is the content security policy of a frontend: its own origin
// only. A product that needs more wraps its assets in its own handler.
const frontendCSP = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
	"frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

// Frontend is static assets the product serves: a single page application,
// a documentation site. Requests the page makes to the product's endpoints
// are attributed to it in the diagram.
type Frontend struct {
	nodeBase
	prefix string
	root   string
	fsys   fs.FS
	files  int
}

// StaticConfigurer configures a frontend.
type StaticConfigurer interface {
	staticConfigure(o *staticOptions)
}

type staticOptions struct {
	root string
}

type staticOption func(o *staticOptions)

// statusRecorder remembers the status a handler sent.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// staticConfigure sets the option on what it configures.
func (f staticOption) staticConfigure(o *staticOptions) { f(o) }

// Root serves the sub-directory dir of the file system rather than its root —
// "assets" for a //go:embed assets directive.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Root(dir string) StaticConfigurer {
	return staticOption(func(o *staticOptions) { o.root = dir })
}

// Static declares a frontend serving fsys under prefix, which must end with
// '/'. A path with no file extension that matches no file serves index.html,
// so client-side routing works; a missing file with an extension — a script,
// a stylesheet — stays a 404. A directory is never listed. The SDK's static
// handler serves it; kit draws it and observes every request.
//
//go:noinline
func (s *Service) Static(name, prefix string, fsys fs.FS, opts ...StaticConfigurer) *Frontend {
	var o staticOptions
	for _, opt := range opts {
		opt.staticConfigure(&o)
	}
	f := &Frontend{prefix: prefix, root: o.root}
	f.kind, f.name, f.decl = model.KindFrontend, name, callerPos()
	s.add(f, true)
	if fsys == nil {
		s.problem(f.decl, f.id, "static.nil", "name", name)
		return f
	}
	if !strings.HasPrefix(prefix, "/") || !strings.HasSuffix(prefix, "/") || strings.HasPrefix(prefix, "/_kit/") {
		s.problem(f.decl, f.id, "static.prefix", "name", name, "prefix", prefix)
		return f
	}
	f.fsys = fsys
	if o.root != "" {
		sub, err := fs.Sub(fsys, o.root)
		if err != nil {
			s.problem(f.decl, f.id, "static.root", "name", name, "root", o.root)
			return f
		}
		f.fsys = sub
	}
	f.files = countFiles(f.fsys)
	if _, err := fs.Stat(f.fsys, "index.html"); err != nil {
		s.warn(f.decl, f.id, "static.index", "name", name)
	}
	return f
}

// served is where app a serves the frontend: its prefix under its module's
// (ADR 0008), as declared for the product's own.
func (f *Frontend) served(a *App) string {
	prefix, _ := a.prefixOf(f.svc)
	return model.UnderPrefix(prefix, f.prefix)
}

// mount registers the frontend's routes on r, or says why it cannot.
func (f *Frontend) mount(a *App, r *routes) phrase {
	if f.fsys == nil {
		return phrase{}
	}
	newFiles := plug.NewStaticFiles.Load()
	if newFiles == nil {
		return say("static.unservable", "name", f.name, "detail", "framework/kit/server is not imported")
	}
	files, err := (*newFiles)(f.fsys, frontendCSP)
	if err != nil {
		return say("static.unservable", "name", f.name, "detail", errs.PublicOf(err))
	}
	prefix := f.served(a)
	h := http.StripPrefix(strings.TrimSuffix(prefix, "/"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, sp := a.begin(r.Context(), &spanStart{node: f.id, from: model.ExternalID, edge: model.EdgeCalls, op: model.OpRequest, name: "GET " + prefix})
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		files.ServeHTTP(rec, r.WithContext(ctx))
		sp.attr("http.status", strconv.Itoa(rec.status))
		var err error
		if rec.status >= http.StatusInternalServerError {
			err = Unavailable("the asset could not be served")
		}
		sp.end(err)
	}))
	return r.handle("GET "+prefix, f.id, h)
}

// describe fills the graph node out with what the Frontend declares, and
// returns its edges.
func (f *Frontend) describe(a *App, out *model.Node) []model.Edge {
	out.Frontend = &model.FrontendInfo{Prefix: f.served(a), Files: f.files}
	// A frontend is there to be loaded: the outside world's browsers fetch
	// it. True by construction, so declared — not only once a browser has.
	return []model.Edge{{From: model.ExternalID, To: f.id, Kind: model.EdgeCalls, Declared: true}}
}

// WriteHeader records the status and writes it.
func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap is the writer the recorder wraps, for http.ResponseController.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// countFiles is how many regular files fsys holds; a directory it cannot read
// counts none, which the graph shows as it is.
func countFiles(fsys fs.FS) int {
	n := 0
	walk := func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	}
	if err := fs.WalkDir(fsys, ".", walk); err != nil {
		return n
	}
	return n
}
