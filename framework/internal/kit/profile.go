package kit

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/framework/model"
)

// serveHeapProfile answers GET /_kit/api/profile/heap: the live heap, after
// a collection, folded onto the graph.
func (a *App) serveHeapProfile(w http.ResponseWriter, r *http.Request) {
	prof := plug.StudioProfiler.Load()
	if prof == nil {
		a.replyError(r.Context(), w, NotFound("the Studio is not linked: import framework/kit/studio"))
		return
	}
	out, err := prof.Heap(a.Graph(), a.sourceOf)
	if err != nil {
		a.replyError(r.Context(), w, failure(CodeProfileRead, "PROFILE_UNREADABLE", "the heap profile could not be read", err))
		return
	}
	out.At = a.clock.Now().UTC()
	writeJSON(w, http.StatusOK, out)
}

// serveGoroutines answers GET /_kit/api/goroutines: every goroutine, grouped
// by the node it works for, the loop it belongs to, what it waits on, and
// its innermost frame that is not the runtime's.
func (a *App) serveGoroutines(w http.ResponseWriter, r *http.Request) {
	prof := plug.StudioProfiler.Load()
	if prof == nil {
		a.replyError(r.Context(), w, NotFound("the Studio is not linked: import framework/kit/studio"))
		return
	}
	out, err := prof.Goroutines(a.clock.Now().UTC())
	if err != nil {
		a.replyError(r.Context(), w, failure(CodeProfileRead, "PROFILE_UNREADABLE", "the goroutines could not be listed", err))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// sourceOf is moduleSource for the profiler, which knows a function by its
// file, its line and its name.
func (a *App) sourceOf(file string, line int, fn string) *model.Source {
	return a.moduleSource(posAt(file, line, fn, ""))
}

// moduleSource locates a function of the product's own module, relative to
// its root; any other file — the standard library, a dependency — has no
// source here, so no path outside the module is ever disclosed.
func (a *App) moduleSource(p pos) *model.Source {
	if p.file() == "" {
		return nil
	}
	file := ""
	switch {
	case a.root != "" && filepath.IsAbs(p.file()):
		if rel, err := filepath.Rel(a.root, p.file()); err == nil && filepath.IsLocal(rel) {
			file = filepath.ToSlash(rel)
		}
	case a.module != "" && strings.HasPrefix(p.file(), a.module+"/"):
		file = strings.TrimPrefix(p.file(), a.module+"/")
	}
	if file == "" {
		return nil
	}
	return &model.Source{File: file, Line: p.line(), Func: p.fn()}
}
