// Package main — the docs/api document of one module: its packages and
// exported symbols merged across the cells, sorted, encoded byte for byte the
// same on every machine.
package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
)

// apiFormat names the document's shape; docs/api/schema.json describes it. It
// changes only with an incompatible change of the shape.
const apiFormat string = "sdk.api/v1"

// apiDocument is docs/api/<module>.json: one module's exported API, judged
// on every cell.
type apiDocument struct {
	// Format is apiFormat.
	Format string `json:"format"`
	// Module is the module's path.
	Module string `json:"module"`
	// Dir is the module's directory, relative to the repository's root.
	Dir string `json:"dir"`
	// Cells are the cells the API was read on, in the platforms table's
	// order; a record without platforms holds on all of them.
	Cells []string `json:"cells"`
	// Packages are the module's packages, by import path.
	Packages []apiPackage `json:"packages"`
	// Symbols are the module's exported symbols, in apiSymbolOrder.
	Symbols []apiSymbol `json:"symbols"`
}

// builtAPI is a repository's API, read from the code.
type builtAPI struct {
	// docs are the modules' documents by name.
	docs map[string]*apiDocument
	// names are the documents' names, sorted.
	names []string
	// cells holds, per cell, the symbols of each package directory: what the
	// pin markers of that directory are compared with.
	cells []map[string][]apiSymbol
	// pkgDocs holds, per cell, the package comment of each package
	// directory: what a pin file's package marker is compared with.
	pkgDocs []map[string]string
	// dirs are the project's package directories, every one go list names on
	// any cell — one with no file there, or with tests alone, included.
	dirs []packageDir
}

// packageDir is a project package's directory and import path.
type packageDir struct {
	// dir is the absolute directory.
	dir string
	// path is the import path.
	path string
}

// apiPackage is one package of a module, on the cells it holds on.
type apiPackage struct {
	// Path is its import path.
	Path string `json:"path"`
	// Name is its package name.
	Name string `json:"name"`
	// Dir is its directory, relative to the module's root.
	Dir string `json:"dir"`
	// Layer is the SDK layer it sits in.
	Layer string `json:"layer,omitempty"`
	// Family is its family within the layer.
	Family string `json:"family,omitempty"`
	// Doc is its package comment: every file's, in file order, as go/doc
	// joins them.
	Doc string `json:"doc,omitempty"`
	// Platforms are the cells this record holds on; absent when it holds on
	// every cell of the document.
	Platforms []string `json:"platforms,omitempty"`
}

// variant is one record and the cells it holds on, as indices into the
// document's cells.
type variant[T any] struct {
	// rec is the record, with no platforms.
	rec T
	// cells are the cells it holds on, ascending.
	cells []int
}

// variants merges one kind of record across cells: a record read
// identically on several cells is one record, and a name that differs
// between cells — another signature, another doc, another file — is one
// record per form, each with its cells.
type variants[T any] struct {
	// byKey are the records by their encoding.
	byKey map[string]*variant[T]
	// order is the keys in the order first seen.
	order []string
}

// newVariants returns an empty merge.
func newVariants[T any]() *variants[T] {
	//: nothing merged yet.
	return &variants[T]{byKey: map[string]*variant[T]{}}
}

// add records rec as holding on cell.
func (v *variants[T]) add(rec T, cell int) error {
	raw, err := json.Marshal(rec)
	//: a record that cannot be encoded cannot be written either.
	if err != nil {
		//: name the failure.
		return fmt.Errorf("encode a record: %w", err)
	}
	key := string(raw)
	got, seen := v.byKey[key]
	//: a record not seen on another cell starts its own variant.
	if !seen {
		got = &variant[T]{rec: rec}
		v.byKey[key] = got
		v.order = append(v.order, key)
	}
	got.cells = append(got.cells, cell)
	//: recorded.
	return nil
}

// list returns the variants in the order first seen.
func (v *variants[T]) list() []*variant[T] {
	out := make([]*variant[T], 0, len(v.order))
	//: one entry per distinct record.
	for _, key := range v.order {
		out = append(out, v.byKey[key])
	}
	//: the variants.
	return out
}

// moduleAPI gathers one module's records across the cells.
type moduleAPI struct {
	// module is the module.
	module listedModule
	// dir is its directory relative to the repository's root.
	dir string
	// packages are its packages' records.
	packages *variants[apiPackage]
	// symbols are its symbols' records.
	symbols *variants[apiSymbol]
}

// newModuleAPI starts an empty module.
func newModuleAPI(m listedModule, dir string) *moduleAPI {
	//: no record yet.
	return &moduleAPI{module: m, dir: dir, packages: newVariants[apiPackage](), symbols: newVariants[apiSymbol]()}
}

// document assembles the module's document: every record with the cells it
// holds on, those that hold on every cell written without, in order.
func (m *moduleAPI) document(cells []platform) *apiDocument {
	names := cellNames(cells)
	pkgs := m.packages.list()
	slices.SortStableFunc(pkgs, func(a, b *variant[apiPackage]) int {
		//: by path, then by the first cell a form holds on.
		return cmp.Or(strings.Compare(a.rec.Path, b.rec.Path), cmp.Compare(a.cells[0], b.cells[0]))
	})
	syms := m.symbols.list()
	slices.SortStableFunc(syms, compareSymbolVariants)
	doc := &apiDocument{
		Format: apiFormat, Module: m.module.Path, Dir: m.dir, Cells: names,
		Packages: make([]apiPackage, 0, len(pkgs)), Symbols: make([]apiSymbol, 0, len(syms)),
	}
	//: each package form, with its cells unless it holds on every one.
	for _, p := range pkgs {
		rec := p.rec
		rec.Platforms = platformsOf(p.cells, names)
		doc.Packages = append(doc.Packages, rec)
	}
	//: each symbol form, likewise.
	for _, s := range syms {
		rec := s.rec
		rec.Platforms = platformsOf(s.cells, names)
		doc.Symbols = append(doc.Symbols, rec)
	}
	//: the document.
	return doc
}

// platformsOf names the cells a record holds on, or nil when it holds on
// every cell.
func platformsOf(cells []int, names []string) []string {
	//: a record of every cell writes no list.
	if len(cells) == len(names) {
		//: absent.
		return nil
	}
	out := make([]string, 0, len(cells))
	//: in the table's order.
	for _, c := range cells {
		out = append(out, names[c])
	}
	//: the cells.
	return out
}

// compareSymbolVariants is the symbols' order: by package; then by the
// top-level name they belong to — a method by its type's —, the declaration
// before its methods; then by name; then by the first cell a form holds on.
func compareSymbolVariants(a, b *variant[apiSymbol]) int {
	//: the order apiSymbolOrder defines, then the first cell.
	return cmp.Or(apiSymbolOrder(&a.rec, &b.rec), cmp.Compare(a.cells[0], b.cells[0]))
}

// apiSymbolOrder orders two symbols as docs/api lists them.
func apiSymbolOrder(a, b *apiSymbol) int {
	//: package, owning name, member after its owner, name, kind, id.
	return cmp.Or(
		strings.Compare(a.Package, b.Package),
		strings.Compare(ownerName(a), ownerName(b)),
		cmp.Compare(memberRank(a), memberRank(b)),
		strings.Compare(a.Name, b.Name),
		strings.Compare(a.Kind, b.Kind),
		strings.Compare(a.ID, b.ID),
	)
}

// ownerName is the top-level name a symbol belongs to: its own, or a method's
// type's.
func ownerName(s *apiSymbol) string {
	//: a method sits under its type.
	if s.Kind == kindMethod {
		//: the type's name.
		return s.Recv
	}
	//: its own name.
	return s.Name
}

// memberRank puts a declaration before its methods.
func memberRank(s *apiSymbol) int {
	//: a method ranks after the type it belongs to.
	if s.Kind == kindMethod {
		//: after.
		return 1
	}
	//: first.
	return 0
}

// encodeDocument writes a document as indented JSON, HTML characters kept as
// they are, one trailing newline: the exact bytes docs/api holds.
func encodeDocument(doc *apiDocument) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	//: one document.
	if err := enc.Encode(doc); err != nil {
		//: name the module that could not be encoded.
		return nil, fmt.Errorf("encode %s: %w", doc.Module, err)
	}
	//: the bytes.
	return buf.Bytes(), nil
}

// documentName is the file a module's document is written to under docs/api,
// without its extension: the module's path below the root module's parent —
// sdk for github.com/kitsunium/sdk, sdk/third-party/aws for its vendor
// module — or the whole path for a module outside it.
func documentName(rootModule, module string) string {
	parent := path.Dir(rootModule)
	//: a module below the root module's parent drops the shared prefix.
	if rest, ok := strings.CutPrefix(module, parent+"/"); ok && parent != "." {
		//: the path below it.
		return rest
	}
	//: the whole path.
	return module
}
