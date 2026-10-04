// Package main — loading the code on one cell: the go command lists the
// cell's whole universe, go/parser reads every file once for every cell, and
// go/types checks every package from source with function bodies ignored.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

const (
	// excludedOnCell is how go list says a package has no file on a cell:
	// such a package is absent there, which is no error.
	excludedOnCell string = "build constraints exclude all Go files"
	// listFields are the fields of `go list -json` the loader reads.
	listFields string = "-json=ImportPath,Dir,Name,GoFiles,Imports,ImportMap,Module,Error,Standard"
	// maxTypeErrors bounds the type errors one failing package reports.
	maxTypeErrors int = 10
	// parseWorkers is how many files of one package are parsed at once.
	parseWorkers int = 8
)

var (
	// listArgs open the go command that lists a cell's universe: every
	// package, its dependencies first, errors reported in the output rather
	// than fatal.
	listArgs = []string{"list", "-e", listFields, "-deps"}

	// pinnedEnv are the variables the loader sets itself on every go command
	// it runs, whatever the caller's environment says: a tag, an experiment or
	// a microarchitecture level the caller exported would change which files
	// a cell compiles, and the result must be the same on every machine.
	pinnedEnv = []string{
		"PWD", "GOOS", "GOARCH", "GOWORK", "GOFLAGS", "CGO_ENABLED", "GOTOOLCHAIN",
		"GOEXPERIMENT", "GOAMD64", "GO386", "GOARM", "GOARM64",
	}
)

// listedModule is a module as `go list -m -json` and `go list -json` report
// it.
type listedModule struct {
	// Path is the module's path.
	Path string
	// Dir is its directory.
	Dir string
	// Main is set for a module of the workspace, or the main module.
	Main bool
	// GoVersion is its go line.
	GoVersion string
}

// listedError is a package's error as go list reports it.
type listedError struct {
	// Err is the message.
	Err string
}

// listedPackage is one package of a cell's universe as go list reports it.
type listedPackage struct {
	// ImportPath is its import path.
	ImportPath string
	// Dir is its directory.
	Dir string
	// Name is its package name.
	Name string
	// GoFiles are the files the cell compiles, by name.
	GoFiles []string
	// Imports are the packages it imports on the cell.
	Imports []string
	// ImportMap maps an import path as written to the package it resolves to.
	ImportMap map[string]string
	// Module is its module, nil for the standard library.
	Module *listedModule
	// Error is its error, when go list could not read it.
	Error *listedError
	// Standard is set for a package of the standard library.
	Standard bool
}

// project reports whether the package belongs to a module of the workspace.
func (p *listedPackage) project() bool {
	//: a workspace module is a main module.
	return p.Module != nil && p.Module.Main
}

// goCommand runs the go command for the loader: in a directory, under the
// pinned environment of one cell.
type goCommand struct {
	// root is the repository's root, where the go command runs.
	root string
	// workspace is the GOWORK value: the root's go.work, or off.
	workspace string
}

// newGoCommand prepares the go command for a repository: workspace mode when
// its root holds a go.work, its root module alone otherwise.
func newGoCommand(root string) goCommand {
	workspace := filepath.Join(root, "go.work")
	//: without a go.work the root module is the only one.
	if _, err := os.Stat(workspace); err != nil {
		workspace = "off"
	}
	//: the command.
	return goCommand{root: root, workspace: workspace}
}

// env is the caller's environment with the pinned variables set for a cell.
func (g goCommand) env(cell platform) []string {
	out := make([]string, 0, len(os.Environ())+len(pinnedEnv))
	//: everything the caller set except what the loader pins.
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		//: a pinned variable is set below, never inherited.
		if !slices.Contains(pinnedEnv, key) {
			out = append(out, kv)
		}
	}
	//: the directory it runs in, the cell, cgo off as every lane builds, the
	//: workspace, a read-only module graph, the local toolchain, and every
	//: build knob at its default.
	return append(out, "PWD="+g.root, "GOOS="+cell.goos, "GOARCH="+cell.goarch, "GOWORK="+g.workspace,
		"GOFLAGS=-mod=readonly", "CGO_ENABLED=0", "GOTOOLCHAIN=local",
		"GOEXPERIMENT=", "GOAMD64=", "GO386=", "GOARM=", "GOARM64=")
}

// run runs `go args…` for a cell and returns its standard output.
func (g goCommand) run(cell platform, args ...string) ([]byte, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = g.root
	cmd.Env = g.env(cell)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	//: a go command that failed says why on its standard error.
	if err != nil {
		//: the command and what it said.
		return nil, fmt.Errorf("go %s (%s): %w: %s", strings.Join(args, " "), cell, err, strings.TrimSpace(stderr.String()))
	}
	//: what it printed.
	return out, nil
}

// modules lists the modules whose packages make the API: every module of
// the workspace, or the root module, sorted by path.
func (g goCommand) modules(cell platform) ([]listedModule, error) {
	out, err := g.run(cell, "list", "-m", "-json")
	//: without the modules there is nothing to read.
	if err != nil {
		//: the go command's failure.
		return nil, err
	}
	var mods []listedModule
	dec := json.NewDecoder(bytes.NewReader(out))
	//: one JSON object per module.
	for {
		var m listedModule
		derr := dec.Decode(&m)
		//: the end of the stream.
		if errors.Is(derr, io.EOF) {
			break
		}
		//: a stream the loader cannot read is a list it cannot trust.
		if derr != nil {
			//: name what failed.
			return nil, fmt.Errorf("go list -m: %w", derr)
		}
		mods = append(mods, m)
	}
	slices.SortFunc(mods, func(a, b listedModule) int { return strings.Compare(a.Path, b.Path) })
	//: the modules.
	return mods, nil
}

// list lists a cell's universe: the packages of the given modules and every
// package they depend on, each after its dependencies.
func (g goCommand) list(cell platform, mods []listedModule) ([]*listedPackage, error) {
	args := slices.Clone(listArgs)
	//: every package of every module.
	for _, m := range mods {
		args = append(args, m.Path+"/...")
	}
	out, err := g.run(cell, args...)
	//: a universe the go command could not list.
	if err != nil {
		//: the go command's failure.
		return nil, err
	}
	//: one JSON object per package.
	return decodePackages(out)
}

// decodePackages reads the stream of package objects go list prints.
func decodePackages(out []byte) ([]*listedPackage, error) {
	var pkgs []*listedPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	//: one object per package, in go list's order.
	for {
		p := &listedPackage{}
		err := dec.Decode(p)
		//: the end of the stream.
		if errors.Is(err, io.EOF) {
			break
		}
		//: a stream the loader cannot read is a list it cannot trust.
		if err != nil {
			//: name what failed.
			return nil, fmt.Errorf("go list: %w", err)
		}
		pkgs = append(pkgs, p)
	}
	//: the universe, dependencies first.
	return pkgs, nil
}

// fileCache parses each file once, whatever cell compiles it: a project's
// file with its comments, which docs/api reads, every other file without. It
// keeps a file only while a cell still to be checked compiles it: the
// listings of every cell are known before the first is checked, and a
// dependency's per-platform files — modernc.org/sqlite carries megabytes per
// GOOS/GOARCH — would otherwise pile up, one set per cell.
type fileCache struct {
	// fset positions every parsed file.
	fset *token.FileSet
	// mu guards files and refs.
	mu sync.Mutex
	// files are the parsed files by absolute name.
	files map[string]*ast.File
	// refs counts, per file, the cells still to be checked that compile it.
	refs map[string]int
}

// newFileCache returns an empty cache that expects the given cells.
func newFileCache(listings [][]*listedPackage) *fileCache {
	c := &fileCache{fset: token.NewFileSet(), files: map[string]*ast.File{}, refs: map[string]int{}}
	//: every file of every cell, once per cell.
	for _, listing := range listings {
		forEachFile(listing, func(path string) { c.refs[path]++ })
	}
	//: one file set for every cell, so positions stay comparable.
	return c
}

// release forgets a checked cell: a file no cell left compiles is dropped.
func (c *fileCache) release(listing []*listedPackage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: every file the cell compiled.
	forEachFile(listing, func(path string) {
		c.refs[path]--
		//: the last cell that needed it.
		if c.refs[path] <= 0 {
			delete(c.files, path)
			delete(c.refs, path)
		}
	})
}

// forEachFile calls fn with the absolute name of every file a listing's
// packages compile.
func forEachFile(listing []*listedPackage, fn func(path string)) {
	//: each package of the cell.
	for _, p := range listing {
		//: each of its files.
		for _, name := range p.GoFiles {
			fn(filepath.Join(p.Dir, name))
		}
	}
}

// parsed returns the package's files on a cell, in GoFiles' order, parsing
// those no cell has parsed yet, several at a time.
func (c *fileCache) parsed(p *listedPackage) ([]*ast.File, error) {
	mode := parser.SkipObjectResolution
	//: docs/api reads a project's comments; nothing reads the others'.
	if p.project() {
		mode |= parser.ParseComments
	}
	paths := make([]string, len(p.GoFiles))
	//: each file by its absolute name.
	for i, name := range p.GoFiles {
		paths[i] = filepath.Join(p.Dir, name)
	}
	out := make([]*ast.File, len(paths))
	errs := make([]error, len(paths))
	jobs := make(chan int)
	var wg sync.WaitGroup
	//: a few parsers share the files the cache does not hold yet.
	for range min(parseWorkers, len(paths)) {
		wg.Go(func() { c.parseJobs(jobs, paths, mode, out, errs) })
	}
	//: every file, by index.
	for i := range paths {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	//: the parse errors fail the package.
	if err := errors.Join(errs...); err != nil {
		//: as the parser reported them.
		return nil, err
	}
	//: the package's files.
	return out, nil
}

// parseJobs parses the files whose indices it receives, or takes them from
// the cache when a cell already parsed them.
func (c *fileCache) parseJobs(jobs <-chan int, paths []string, mode parser.Mode, out []*ast.File, errs []error) {
	//: until the indices run out.
	for i := range jobs {
		//: a file parsed for another cell is the same file.
		if f := c.lookup(paths[i]); f != nil {
			out[i] = f
			continue
		}
		out[i], errs[i] = c.parse(paths[i], mode)
	}
}

// lookup returns a parsed file, or nil.
func (c *fileCache) lookup(path string) *ast.File {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: the cached file, if any.
	return c.files[path]
}

// parse reads and parses one file and caches it.
func (c *fileCache) parse(path string, mode parser.Mode) (*ast.File, error) {
	f, err := parser.ParseFile(c.fset, path, nil, mode)
	//: a file that will not parse cannot be checked.
	if err != nil {
		//: the parser's error names the file and the position.
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.files[path] = f
	//: the parsed file.
	return f, nil
}

// cellImporter serves a cell's packages that are already checked; go list
// orders a package after its dependencies, so every import is.
type cellImporter struct {
	// checked are the cell's checked packages by import path.
	checked map[string]*types.Package
	// importMap maps an import path as written to the package it resolves to.
	importMap map[string]string
}

// Import returns a checked package.
func (m cellImporter) Import(path string) (*types.Package, error) {
	//: unsafe is go/types' own.
	if path == "unsafe" {
		//: the predeclared package.
		return types.Unsafe, nil
	}
	//: a vendored or rewritten path resolves through the map.
	if resolved, ok := m.importMap[path]; ok {
		path = resolved
	}
	pkg, ok := m.checked[path]
	//: an import checked after its importer would mean go list's order
	//: changed, and every result after it would be wrong.
	if !ok {
		//: name the package that is missing.
		return nil, fmt.Errorf("%s is imported before it is checked", path)
	}
	//: the checked package.
	return pkg, nil
}

// checkedPackage is a project package checked on one cell, with what docs/api
// reads of it.
type checkedPackage struct {
	// listed is what go list said of it.
	listed *listedPackage
	// types is the checked package.
	types *types.Package
	// info holds its definitions, uses, implicit imports and expression types.
	info *types.Info
	// files are its files, in GoFiles' order.
	files []*ast.File
}

// checkCell checks every package of a cell's universe from source, function
// bodies ignored, and returns the project's packages in go list's order.
func checkCell(cell platform, pkgs []*listedPackage, cache *fileCache) ([]*checkedPackage, error) {
	checked := map[string]*types.Package{}
	var project []*checkedPackage
	//: every package, after its dependencies.
	for _, p := range pkgs {
		//: unsafe is predeclared; a package with no file on the cell is absent.
		if p.ImportPath == "unsafe" || absentOn(p) {
			continue
		}
		//: any other error of go list is a package the loader cannot read.
		if p.Error != nil {
			//: name the package and the error.
			return nil, fmt.Errorf("%s on %s: %s", p.ImportPath, cell, p.Error.Err)
		}
		cp, err := checkPackage(cell, p, cache, checked)
		//: a package that does not check would leave its API unread.
		if err != nil {
			//: the type errors, by package.
			return nil, err
		}
		checked[p.ImportPath] = cp.types
		//: docs/api reads the project's packages only.
		if p.project() {
			project = append(project, cp)
		}
	}
	//: the project's checked packages.
	return project, nil
}

// absentOn reports whether go list found no file of the package on the cell.
func absentOn(p *listedPackage) bool {
	//: the one error that means "not on this cell".
	return len(p.GoFiles) == 0 && p.Error != nil && strings.Contains(p.Error.Err, excludedOnCell)
}

// checkPackage checks one package from source.
func checkPackage(cell platform, p *listedPackage, cache *fileCache, checked map[string]*types.Package) (*checkedPackage, error) {
	files, perr := cache.parsed(p)
	//: a file that will not parse leaves the package unchecked.
	if perr != nil {
		//: the parse error.
		return nil, perr
	}
	var typeErrs []error
	conf := types.Config{
		Importer:         cellImporter{checked: checked, importMap: p.ImportMap},
		IgnoreFuncBodies: true,
		Sizes:            types.SizesFor("gc", cell.goarch),
		Error: func(err error) {
			//: keep the first few; one is enough to fail.
			if len(typeErrs) < maxTypeErrors {
				typeErrs = append(typeErrs, err)
			}
		},
	}
	//: a module's package is checked at its go line; the standard library at
	//: the toolchain's own.
	if p.Module != nil && p.Module.GoVersion != "" {
		conf.GoVersion = "go" + p.Module.GoVersion
	}
	info := infoFor(p)
	tp, cerr := conf.Check(p.ImportPath, cache.fset, files, info)
	//: a package that does not type-check has no API to read.
	if cerr != nil {
		//: Check returns the first error the handler collected; keep it when
		//: the handler saw none.
		if len(typeErrs) == 0 {
			typeErrs = append(typeErrs, cerr)
		}
		//: the errors collected, under the package's name and the cell's.
		return nil, fmt.Errorf("%s does not type-check on %s: %w", p.ImportPath, cell, errors.Join(typeErrs...))
	}
	//: the checked package.
	return &checkedPackage{listed: p, types: tp, info: info, files: files}, nil
}

// infoFor is the type information kept of a package: what docs/api reads for
// a project's, nothing for any other, which saves the memory of the
// standard library's.
func infoFor(p *listedPackage) *types.Info {
	//: a dependency is only imported.
	if !p.project() {
		//: no information.
		return nil
	}
	//: definitions, uses, implicit imports and the values of expressions.
	return &types.Info{
		Defs:      map[*ast.Ident]types.Object{},
		Uses:      map[*ast.Ident]types.Object{},
		Implicits: map[ast.Node]types.Object{},
		Types:     map[ast.Expr]types.TypeAndValue{},
	}
}
