// Package main — walking a module and parsing each package it holds.
package main

import (
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

var (
	// filepathRel is filepath.Rel, indirected so the render helpers can be
	// tested without a real filesystem layout underneath them.
	filepathRel = filepath.Rel

	// skippedDirs are the directory names the walk never descends into: they
	// hold no package a consumer can import, and testdata in particular holds
	// Go files that deliberately do not compile.
	skippedDirs = map[string]struct{}{
		"vendor": {}, "testdata": {}, "node_modules": {},
	}
)

// collect walks every Go package under the module root and projects every
// exported symbol into a flat row list.
//
// _test.go files are PARSED so Example functions attach to the identifiers they
// document, but their own declarations are skipped by go/doc — an example is
// documentation, a test helper is not.
func collect(opts *indexOptions) (symbols []symbol, err error) {
	root, aerr := filepath.Abs(opts.root)
	//: every path the walk produces is compared against this one, so a root we
	//: cannot resolve makes every later comparison meaningless.
	if aerr != nil {
		//: report the root that could not be resolved.
		return nil, fmt.Errorf("resolve root: %w", aerr)
	}
	//: the caller's options are not mutated; the walk works on a resolved copy.
	resolved := *opts
	resolved.root = root

	var out []symbol
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		//: a directory the walk could not read aborts the run rather than
		//: silently producing a partial index.
		if walkErr != nil {
			//: surface it to WalkDir, which stops the walk.
			return walkErr
		}
		//: only directories carry packages; the files are parsed as a set.
		if !d.IsDir() {
			//: nothing to do for a file.
			return nil
		}
		//: a skipped directory is not descended into at all.
		if skipDir(filepath.Base(path)) {
			//: prune the whole subtree.
			return filepath.SkipDir
		}
		syms, dirErr := loadDir(path, &resolved)
		//: a directory that will not parse is a real problem: the index would
		//: silently lose every symbol it holds.
		if dirErr != nil {
			//: name the directory that failed.
			return fmt.Errorf("load %s: %w", path, dirErr)
		}
		out = append(out, syms...)
		//: this directory contributed whatever it had.
		return nil
	})
	//: the first directory failure aborts the whole index.
	if walkErr != nil {
		//: surface it unchanged; it already names the directory.
		return nil, walkErr
	}
	//: every package under the root, flattened.
	return out, nil
}

// skipDir reports whether a directory name is one the walk never descends into.
func skipDir(base string) bool {
	//: a dot-prefixed directory is version control, editor state or a cache.
	if strings.HasPrefix(base, ".") {
		//: prune it.
		return true
	}
	_, skipped := skippedDirs[base]
	//: vendored, generated or deliberately-broken trees.
	return skipped
}

// loadDir parses a single directory and returns its exported symbols.
//
// It returns no rows and no error for a directory that holds no Go files, or
// one whose import path is internal: an internal package is not visible to
// consumers, so indexing it would offer a search result nobody can import.
func loadDir(dir string, opts *indexOptions) (symbols []symbol, err error) {
	fset := token.NewFileSet()
	pkgs, perr := parser.ParseDir(fset, dir, keepGoFiles, parser.ParseComments)
	//: a directory that will not parse cannot be indexed honestly.
	if perr != nil {
		//: surface the parse failure to the walk.
		return nil, perr
	}

	importPath, ierr := importPathOf(dir, opts)
	//: a path we cannot make relative to the root cannot be named.
	if ierr != nil {
		//: surface it to the walk.
		return nil, ierr
	}
	//: internal packages are not visible to consumers; do not index them.
	if strings.Contains("/"+importPath+"/", "/internal/") {
		//: no rows, and not an error.
		return nil, nil
	}

	//: importPathOf already resolved this successfully, so the second call
	//: cannot fail — but the result is what labels every row, so it is taken
	//: from the same place rather than recomputed by hand.
	rel, rerr := relPath(dir, opts.root)
	//: unreachable in practice, but a row labelled with a path we did not
	//: compute would point the docs site at the wrong package.
	if rerr != nil {
		//: surface it to the walk.
		return nil, rerr
	}
	var out []symbol
	//: one production package per directory, plus any external test package.
	for name, pkg := range pkgs {
		//: the external test package documents nothing a consumer imports.
		if strings.HasSuffix(name, "_test") {
			continue
		}
		syms, derr := packageSymbols(pkg, fset, importPath, rel, opts)
		//: go/doc only fails on a malformed file set, which the parser accepted.
		if derr != nil {
			//: surface it to the walk.
			return nil, derr
		}
		out = append(out, syms...)
	}
	//: whatever the directory's production package exported.
	return out, nil
}

// keepGoFiles selects the files parser.ParseDir reads.
func keepGoFiles(fi os.FileInfo) bool {
	//: include _test.go too, so Example functions attach to what they document.
	return strings.HasSuffix(fi.Name(), ".go")
}

// packageSymbols builds the doc.Package for one parsed package and projects it.
func packageSymbols(pkg *ast.Package, fset *token.FileSet, importPath, rel string, opts *indexOptions) (symbols []symbol, err error) {
	//: go/doc wants the file set, so a stable order keeps the output diffable.
	files := slices.Collect(maps.Values(pkg.Files))
	//: omit doc.AllDecls — without it, doc.New keeps only exported identifiers,
	//: which is exactly the surface a consumer sees on pkg.go.dev. Internal
	//: helpers are not indexable from a consumer's perspective.
	dp, derr := doc.NewFromFiles(fset, files, importPath)
	//: a doc build failure means the package cannot be projected.
	if derr != nil {
		//: surface it to the caller.
		return nil, derr
	}
	//: every exported declaration, as index rows.
	return emit(dp, fset, rel, opts), nil
}

// importPathOf builds the module-relative import path of a directory.
func importPathOf(dir string, opts *indexOptions) (importPath string, err error) {
	rel, rerr := relPath(dir, opts.root)
	//: a directory outside the root has no module-relative path.
	if rerr != nil {
		//: surface it to the caller.
		return "", rerr
	}
	//: the module root itself carries the bare module path.
	if rel == "." || rel == "" {
		//: no segment to append.
		return opts.modulePath, nil
	}
	//: everything else hangs off it.
	return opts.modulePath + "/" + rel, nil
}

// relPath returns dir relative to root, in slash form.
func relPath(dir, root string) (rel string, err error) {
	out, rerr := filepath.Rel(root, dir)
	//: a directory outside the root cannot be made relative to it.
	if rerr != nil {
		//: surface it to the caller.
		return "", rerr
	}
	//: import paths and URLs both use forward slashes, whatever the host does.
	return filepath.ToSlash(out), nil
}
