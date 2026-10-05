package kit

import (
	"path"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/kitsunium/sdk/framework/model"
)

// Where code comes from. A declaration's package says which Go module of the
// build it belongs to: the product's, a module's (ADR 0008), whose services
// take declarations from their own Go module only, or a library's. The graph
// says a position outside the product's Go module — a module's, a
// library's — relative to that Go module's root.

// goModule is one Go module of the running binary.
type goModule struct {
	// path is its module path; id how a position names it: "path@version",
	// or the path alone for a module built from a directory.
	path, id string
}

// buildModules are the Go modules the running binary was built from, the
// main module first; nil when the binary carries no build information.
var buildModules = sync.OnceValue(func() []goModule {
	bi := readBuild()
	if bi == nil {
		return nil
	}
	out := []goModule{{path: bi.Main.Path, id: bi.Main.Path}}
	for _, d := range bi.Deps {
		out = append(out, goModule{path: d.Path, id: goModuleID(d)})
	}
	return out
})

// goModuleID names a dependency as the analyzer does: its path and the
// version the build used — a replacement's own —, or its path alone when a
// directory (a replace, a workspace) holds it.
func goModuleID(d *debug.Module) string {
	v := d.Version
	if r := d.Replace; r != nil {
		if r.Version == "" || r.Version == "(devel)" {
			return d.Path
		}
		v = r.Version
	}
	if v == "" || v == "(devel)" {
		return d.Path
	}
	return d.Path + "@" + v
}

// goModuleOf is the Go module of the build that holds package pkg: the one
// whose path is the longest prefix of it; the main package is the main
// module's.
func goModuleOf(pkg string) (goModule, bool) {
	mods := buildModules()
	if pkg == "" || len(mods) == 0 {
		return goModule{}, false
	}
	if pkg == "main" {
		return mods[0], true
	}
	best, found := goModule{}, false
	for _, m := range mods {
		if (pkg == m.path || strings.HasPrefix(pkg, m.path+"/")) && len(m.path) > len(best.path) {
			best, found = m, true
		}
	}
	return best, found
}

// packageDir is where package pkg lies inside Go module mod, slash-separated:
// "intake" for github.com/kitsunium/moderation/intake. A test package's
// "_test" is its directory's.
func packageDir(pkg, mod string) string {
	rel := strings.TrimPrefix(strings.TrimPrefix(pkg, mod), "/")
	return strings.TrimSuffix(rel, "_test")
}

// moduleRoots are the directories the Go modules outside the product's lie
// in on this machine, by id, as positions revealed them: the source
// endpoint reads their files there, a module's or a library's.
type moduleRoots struct {
	mu    sync.Mutex
	roots map[string]string
}

// set remembers where Go module id lies, the first time a position says it.
func (r *moduleRoots) set(id, dir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.roots == nil {
		r.roots = map[string]string{}
	}
	if _, known := r.roots[id]; !known {
		r.roots[id] = dir
	}
}

// get is the root directory of the Go module id, when a position revealed it.
func (r *moduleRoots) get(id string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	dir, ok := r.roots[id]
	return dir, ok
}

// goModuleSource says a position in code outside the product's Go module —
// a module's, a library's —: its file relative to its Go module's root,
// which it remembers for the source endpoint. nil for the product's own
// code, or when the runtime does not say the package.
func (a *App) goModuleSource(p *pos) *model.Source {
	if a == nil {
		return nil
	}
	pkg := p.pkg()
	if pkg == "" && p.fn() != "" {
		pkg, _ = packageOf(p.fn())
	}
	m, ok := goModuleOf(pkg)
	if !ok || m.path == a.module || a.module == "" {
		return nil
	}
	dir := packageDir(pkg, m.path)
	a.rememberRoot(m, dir, p.file())
	return &model.Source{File: moduleFile(dir, p.file()), Line: p.line(), Func: p.fn(), GoModule: m.id}
}

// moduleFile is the file a graph names for a position in package directory
// dir of a Go module: slash-separated, relative to the Go module's root,
// whatever the machine's separator.
func moduleFile(dir, file string) string {
	base := path.Base(filepath.ToSlash(file))
	if dir == "" {
		return base
	}
	return dir + "/" + base
}

// rememberRoot keeps where Go module m lies on this machine: the package's
// directory is its path dir inside the Go module, so what is left of the
// file's directory is the Go module's root — when it ends as it must.
func (a *App) rememberRoot(m goModule, dir, file string) {
	at, inside := filepath.Dir(file), string(filepath.Separator)+filepath.FromSlash(dir)
	if filepath.IsAbs(file) && (dir == "" || strings.HasSuffix(at, inside)) {
		a.roots.set(m.id, strings.TrimSuffix(at, inside))
	}
}
