// Package main — the two API modes: -write-api writes docs/api from the code,
// -check-api regenerates it in memory and fails on any byte that differs,
// and, when asked, on the pin markers and the generated files' digests.
package main

import (
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

const (
	// apiDir is where the documents are written, relative to the root.
	apiDir string = "docs/api"
	// apiExt is a document's extension.
	apiExt string = ".json"
	// schemaFile is the format's description, which is no module's document.
	schemaFile string = "schema.json"
	// apiFilePerm is a document's mode: the files are committed and public.
	apiFilePerm os.FileMode = 0o644
	// apiDirPerm is the mode docs/api's directories are created with.
	apiDirPerm os.FileMode = 0o755
	// apiCheckAdvice ends a failed check with its one remedy.
	apiCheckAdvice string = "docs/api is written from the code: run `make api` and commit the result"
)

// apiOptions are the parameters of the API modes.
type apiOptions struct {
	// root is the repository's root.
	root string
	// cells are the platforms table's cells; the first is the reference.
	cells []platform
	// markers makes -check-api compare every cell's symbols with the pin
	// markers of api_gen*_test.go files.
	markers bool
	// digests makes -check-api verify every generated file's header digests
	// against the design files' bytes.
	digests bool
}

// realRoot is a repository's root as the go command reports its directories:
// absolute, every symbolic link resolved — a macOS temporary directory is
// one — so a directory go list names compares with it.
func realRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	//: a root that does not resolve.
	if err != nil {
		//: name it.
		return "", fmt.Errorf("resolve %s: %w", root, err)
	}
	real, err := filepath.EvalSymlinks(abs)
	//: a root that does not exist.
	if err != nil {
		//: name it.
		return "", fmt.Errorf("resolve %s: %w", root, err)
	}
	//: the real path.
	return real, nil
}

// buildAPI reads a repository's exported API on every cell; o.root must be
// real (realRoot).
func buildAPI(o apiOptions) (*builtAPI, error) {
	gocmd := newGoCommand(o.root)
	mods, err := mainModules(gocmd, o.cells[0])
	//: without a module there is no API to read.
	if err != nil {
		//: the go command's failure.
		return nil, err
	}
	listings, err := listCells(gocmd, o.cells, mods)
	//: a cell whose universe could not be listed cannot be judged.
	if err != nil {
		//: the first failure.
		return nil, err
	}
	b := newAPIBuilder(o.root, mods, newFileCache(listings))
	b.noteDirs(listings)
	//: one cell at a time: a cell's universe is dropped before the next.
	for i, cell := range o.cells {
		//: a cell that does not check fails the whole API.
		if cerr := b.readCell(i, cell, listings[i]); cerr != nil {
			//: the cell's failure.
			return nil, cerr
		}
	}
	//: the documents and the per-cell symbols.
	return b.result(o.cells)
}

// mainModules lists the modules whose packages make the API.
func mainModules(gocmd goCommand, cell platform) ([]listedModule, error) {
	mods, err := gocmd.modules(cell)
	//: the go command's failure.
	if err != nil {
		//: as it is.
		return nil, err
	}
	main := slices.DeleteFunc(mods, func(m listedModule) bool { return !m.Main })
	//: a repository with no module has no API, and saying so beats an empty
	//: docs/api.
	if len(main) == 0 {
		//: refuse.
		return nil, fmt.Errorf("go list -m found no module under %s", gocmd.root)
	}
	//: the workspace's modules.
	return main, nil
}

// listCells lists every cell's universe at once: the go command runs per
// cell, in parallel.
func listCells(gocmd goCommand, cells []platform, mods []listedModule) ([][]*listedPackage, error) {
	out := make([][]*listedPackage, len(cells))
	errs := make([]error, len(cells))
	var wg sync.WaitGroup
	//: one go list per cell.
	for i, cell := range cells {
		wg.Go(func() { out[i], errs[i] = gocmd.list(cell, mods) })
	}
	wg.Wait()
	//: the first cell that failed.
	for _, err := range errs {
		//: fail on it.
		if err != nil {
			//: as the go command said it.
			return nil, err
		}
	}
	//: every cell's universe.
	return out, nil
}

// apiBuilder gathers the records of every cell, module by module.
type apiBuilder struct {
	// root is the repository's root.
	root string
	// rootModule is the path of the module at the root, which names the
	// documents.
	rootModule string
	// modules are the modules' records by path.
	modules map[string]*moduleAPI
	// cache parses each file once for every cell.
	cache *fileCache
	// byDir holds, per cell, the symbols of each package directory.
	byDir []map[string][]apiSymbol
	// pkgDocs holds, per cell, the package comment of each package
	// directory: what a pin file's package marker is compared with.
	pkgDocs []map[string]string
	// dirs are the project's package directories and their import paths.
	dirs map[string]string
}

// newAPIBuilder prepares the records of the given modules.
func newAPIBuilder(root string, mods []listedModule, cache *fileCache) *apiBuilder {
	b := &apiBuilder{root: root, modules: map[string]*moduleAPI{}, cache: cache, dirs: map[string]string{}}
	//: every module gets its records, and the root's names the documents.
	for _, m := range mods {
		dir := relSlash(root, m.Dir)
		b.modules[m.Path] = newModuleAPI(m, dir)
		//: the module at the root.
		if dir == "." {
			b.rootModule = m.Path
		}
	}
	//: ready for the first cell.
	return b
}

// noteDirs records every project package directory any cell lists, so a
// directory holding pins and no package any more is still judged.
func (b *apiBuilder) noteDirs(listings [][]*listedPackage) {
	//: every cell's universe.
	for _, listing := range listings {
		//: its project packages.
		for _, p := range listing {
			//: a dependency is no part of the project.
			if p.project() {
				b.dirs[p.Dir] = p.ImportPath
			}
		}
	}
}

// readCell checks one cell's universe and records its project's API.
func (b *apiBuilder) readCell(index int, cell platform, listing []*listedPackage) error {
	project, err := checkCell(cell, listing, b.cache)
	//: the cell does not check.
	if err != nil {
		//: as checkCell says it.
		return err
	}
	codes := map[string]*apiCode{}
	byDir := map[string][]apiSymbol{}
	docs := map[string]string{}
	//: every project package, after its dependencies, so a re-exported
	//: sentinel finds its code.
	for _, cp := range project {
		//: a package outside every module read is none of the API's.
		if rerr := b.readPackage(index, cp, codes, byDir, docs); rerr != nil {
			//: name it.
			return rerr
		}
	}
	b.byDir = append(b.byDir, byDir)
	b.pkgDocs = append(b.pkgDocs, docs)
	b.cache.release(listing)
	//: the cell is recorded.
	return nil
}

// readPackage records one package's record and symbols on a cell.
func (b *apiBuilder) readPackage(index int, cp *checkedPackage, codes map[string]*apiCode, byDir map[string][]apiSymbol, docs map[string]string) error {
	m, ok := b.modules[cp.listed.Module.Path]
	//: go list only lists main modules' packages as project packages.
	if !ok {
		//: a module the list of modules did not name.
		return fmt.Errorf("%s belongs to %s, which go list -m did not name", cp.listed.ImportPath, cp.listed.Module.Path)
	}
	place := placeOf(relSlash(b.root, cp.listed.Dir))
	r := &pkgReader{pkg: cp.types, info: cp.info, files: cp.files, modDir: m.module.Dir, place: place, codes: codes}
	syms := r.readSymbols(b.cache.fset)
	//: every symbol, on this cell.
	for _, s := range syms {
		//: a record json cannot encode is a defect of this program.
		if err := m.symbols.add(s, index); err != nil {
			//: name it.
			return err
		}
	}
	byDir[cp.listed.Dir] = syms
	record := packageRecord(cp, m.module.Dir, place)
	docs[cp.listed.Dir] = record.Doc
	//: and the package itself.
	return m.packages.add(record, index)
}

// packageRecord is a package's own record on a cell: its name, directory,
// place and package comment.
func packageRecord(cp *checkedPackage, modDir string, place pkgPlace) apiPackage {
	var doc strings.Builder
	//: every file's package comment, joined as go/doc joins them.
	for _, f := range cp.files {
		//: a file without one adds nothing.
		if text := f.Doc.Text(); text != "" {
			//: a newline between two comments.
			if doc.Len() > 0 {
				doc.WriteString("\n")
			}
			doc.WriteString(text)
		}
	}
	//: the record.
	return apiPackage{
		Path: cp.listed.ImportPath, Name: cp.listed.Name, Dir: relSlash(modDir, cp.listed.Dir),
		Layer: place.layer, Family: place.family, Doc: doc.String(),
	}
}

// result assembles the documents: one per module that holds a package.
func (b *apiBuilder) result(cells []platform) (*builtAPI, error) {
	out := &builtAPI{docs: map[string]*apiDocument{}, cells: b.byDir, pkgDocs: b.pkgDocs}
	//: every module, by path.
	for _, path := range sortedKeys(b.modules) {
		doc := b.modules[path].document(cells)
		//: a module with no package has no API to write.
		if len(doc.Packages) == 0 {
			continue
		}
		name := documentName(b.rootModule, path)
		out.docs[name] = doc
		out.names = append(out.names, name)
	}
	//: an API with no document is a repository the loader did not read.
	if len(out.names) == 0 {
		//: refuse.
		return nil, fmt.Errorf("no module under %s holds a package", b.root)
	}
	slices.Sort(out.names)
	//: every package directory, by directory.
	for _, dir := range sortedKeys(b.dirs) {
		out.dirs = append(out.dirs, packageDir{dir: dir, path: b.dirs[dir]})
	}
	//: the API.
	return out, nil
}

// sortedKeys returns a map's keys, sorted.
func sortedKeys[V any](m map[string]V) []string {
	//: every key, in order.
	return slices.Sorted(maps.Keys(m))
}

// documentPath is the file a named document is written to.
func documentPath(root, name string) string {
	//: docs/api/<name>.json.
	return filepath.Join(root, filepath.FromSlash(apiDir), filepath.FromSlash(name)+apiExt)
}

// runWriteAPI writes docs/api from the code and removes a document no module
// writes any more. It returns the process's exit status.
func runWriteAPI(o apiOptions, out io.Writer) int {
	root, rerr := realRoot(o.root)
	//: a repository that does not resolve has no API.
	if rerr != nil {
		fmt.Fprintf(out, "genindex: %v\n", rerr)
		//: a failed run.
		return 1
	}
	o.root = root
	api, err := buildAPI(o)
	//: an API that could not be read is not written in part.
	if err != nil {
		fmt.Fprintf(out, "genindex: %v\n", err)
		//: a failed run.
		return 1
	}
	//: every module's document.
	for _, name := range api.names {
		//: a document that could not be written fails the run.
		if werr := writeDocument(o.root, name, api.docs[name], out); werr != nil {
			fmt.Fprintf(out, "genindex: %v\n", werr)
			//: a failed run.
			return 1
		}
	}
	//: a module that is gone takes its document with it.
	if rerr := removeStale(o.root, api.names, out); rerr != nil {
		fmt.Fprintf(out, "genindex: %v\n", rerr)
		//: a failed run.
		return 1
	}
	//: written.
	return 0
}

// writeDocument encodes and writes one document.
func writeDocument(root, name string, doc *apiDocument, out io.Writer) error {
	raw, err := encodeDocument(doc)
	//: a document that cannot be encoded.
	if err != nil {
		//: as the encoder said it.
		return err
	}
	target := documentPath(root, name)
	//: the directory of a nested module's document.
	if merr := os.MkdirAll(filepath.Dir(target), apiDirPerm); merr != nil {
		//: name the directory.
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), merr)
	}
	//: the document's bytes.
	if werr := os.WriteFile(target, raw, apiFilePerm); werr != nil {
		//: name the file.
		return fmt.Errorf("write %s: %w", target, werr)
	}
	fmt.Fprintf(out, "[genindex] wrote %s/%s%s (%d packages, %d symbols)\n", apiDir, name, apiExt, len(doc.Packages), len(doc.Symbols))
	//: written.
	return nil
}

// removeStale deletes every document under docs/api that names no module
// written now; the schema is no document.
func removeStale(root string, names []string, out io.Writer) error {
	existing, err := existingDocuments(root)
	//: a docs/api that cannot be read cannot be cleaned.
	if err != nil {
		//: as the walk said it.
		return err
	}
	//: every document no module wrote.
	for _, name := range existing {
		//: a current module's document stays.
		if slices.Contains(names, name) {
			continue
		}
		//: a stale one goes.
		if rerr := os.Remove(documentPath(root, name)); rerr != nil {
			//: name the file.
			return fmt.Errorf("remove %s: %w", documentPath(root, name), rerr)
		}
		fmt.Fprintf(out, "[genindex] removed %s/%s%s: no module writes it\n", apiDir, name, apiExt)
	}
	//: docs/api holds the current modules' documents only.
	return nil
}

// existingDocuments names every document under docs/api, the schema left out.
func existingDocuments(root string) ([]string, error) {
	base := filepath.Join(root, filepath.FromSlash(apiDir))
	var names []string
	walkErr := filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		//: a docs/api that does not exist yet holds no document.
		if os.IsNotExist(err) && p == base {
			//: nothing to walk.
			return filepath.SkipDir
		}
		//: any other failure stops the walk.
		if err != nil {
			//: as WalkDir said it.
			return err
		}
		//: a JSON file other than the schema is a document.
		if !d.IsDir() && strings.HasSuffix(p, apiExt) && p != filepath.Join(base, schemaFile) {
			rel := relSlash(base, p)
			names = append(names, strings.TrimSuffix(rel, apiExt))
		}
		//: on to the next entry.
		return nil
	})
	//: a walk that failed.
	if walkErr != nil {
		//: name the directory.
		return nil, fmt.Errorf("read %s: %w", base, walkErr)
	}
	slices.Sort(names)
	//: the documents.
	return names, nil
}
