package kit

import (
	"path/filepath"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
)

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
