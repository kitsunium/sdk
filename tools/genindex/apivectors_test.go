// Package main — the vectors of docs/api/schema.json: the go: id grammar, and
// the canonical signatures of a small universe. kit reads the same vectors,
// so a reader of docs/api and the writer agree on every string they share.
package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// schemaPath is docs/api/schema.json relative to this package; a Bazel test
// finds it there as a data dependency.
const schemaPath string = "../../docs/api/schema.json"

// schemaVectors is the part of the schema a test reads: its x-vectors.
type schemaVectors struct {
	// Vectors are the schema's test vectors.
	Vectors struct {
		// GoID are the id grammar's vectors.
		GoID struct {
			// Valid are ids with their parts.
			Valid []goIDVector `json:"valid"`
			// Invalid are strings the grammar refuses.
			Invalid []string `json:"invalid"`
		} `json:"goid"`
		// Canonical are universes and the symbols they export.
		Canonical []canonicalVector `json:"canonical"`
	} `json:"x-vectors"`
}

// goIDVector is a valid id and its parts.
type goIDVector struct {
	// ID is the id.
	ID string `json:"id"`
	// Path is the import path, unescaped.
	Path string `json:"path"`
	// Recv is a method's type.
	Recv string `json:"recv"`
	// Pointer marks a pointer receiver.
	Pointer bool `json:"pointer"`
	// RecvGeneric marks a generic receiver.
	RecvGeneric bool `json:"recvGeneric"`
	// Name is the symbol's name.
	Name string `json:"name"`
	// Generic marks a generic function or method.
	Generic bool `json:"generic"`
}

// canonicalVector is a universe of packages and every symbol it exports.
type canonicalVector struct {
	// About says what the vector covers.
	About string `json:"about"`
	// Packages are the universe, dependencies first.
	Packages []vectorPackage `json:"packages"`
	// Symbols are every exported symbol of the universe.
	Symbols []vectorSymbol `json:"symbols"`
}

// vectorPackage is one package of a vector's universe.
type vectorPackage struct {
	// Path is its import path.
	Path string `json:"path"`
	// Files are its files by name.
	Files map[string]string `json:"files"`
}

// vectorSymbol is what a vector pins of a symbol.
type vectorSymbol struct {
	// ID is its go: id.
	ID string `json:"id"`
	// Kind is its kind.
	Kind string `json:"kind"`
	// Spelled is its signature as its file writes it.
	Spelled string `json:"spelled"`
	// Canonical is its canonical signature.
	Canonical string `json:"canonical"`
	// Owner is an alias's owner.
	Owner string `json:"owner,omitempty"`
}

// readSchemaVectors reads the schema's vectors.
func readSchemaVectors(t *testing.T) schemaVectors {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(schemaPath))
	if err != nil {
		t.Fatalf("reading the schema: %v", err)
	}
	var v schemaVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decoding the schema: %v", err)
	}
	return v
}

// Test_goIDVectors pins the id grammar to the schema's vectors, which are
// platform's design.GoID vectors unchanged: every valid id parses into its
// parts and is written back byte for byte; every invalid one is refused.
func Test_goIDVectors(t *testing.T) {
	t.Parallel()
	v := readSchemaVectors(t)
	if len(v.Vectors.GoID.Valid) == 0 || len(v.Vectors.GoID.Invalid) == 0 {
		t.Fatal("the schema carries no id vector")
	}
	for _, c := range v.Vectors.GoID.Valid {
		t.Run(c.ID, func(t *testing.T) {
			t.Parallel()
			got, err := parseGoID(c.ID)
			if err != nil {
				t.Fatalf("parseGoID(%q) = %v, want its parts", c.ID, err)
			}
			want := goID{
				path: c.Path, recv: c.Recv, name: c.Name,
				flags: flagIf(c.Pointer, idPointer) | flagIf(c.RecvGeneric, idRecvGeneric) | flagIf(c.Generic, idGeneric),
			}
			if got != want {
				t.Fatalf("parseGoID(%q) = %+v, want %+v", c.ID, got, want)
			}
			if back := got.String(); back != c.ID {
				t.Fatalf("the parts write back %q, want %q", back, c.ID)
			}
		})
	}
	for _, s := range v.Vectors.GoID.Invalid {
		t.Run("invalid "+s, func(t *testing.T) {
			t.Parallel()
			if got, err := parseGoID(s); err == nil {
				t.Fatalf("parseGoID(%q) = %+v, want a refusal", s, got)
			}
		})
	}
}

// Test_canonicalVectors pins the records the writer reads of a universe to
// the schema's vectors: every exported symbol, with its id, kind, spelled
// and canonical signatures and an alias's owner — nothing more, nothing less.
// When they differ the test prints what the writer read, in the vectors'
// form, so a deliberate change is reviewed rather than retyped.
func Test_canonicalVectors(t *testing.T) {
	t.Parallel()
	v := readSchemaVectors(t)
	if len(v.Vectors.Canonical) == 0 {
		t.Fatal("the schema carries no canonical vector")
	}
	for i, c := range v.Vectors.Canonical {
		t.Run(c.About[:min(len(c.About), 40)], func(t *testing.T) {
			t.Parallel()
			got := vectorSymbols(t, readUniverse(t, c.Packages))
			want := slices.Clone(c.Symbols)
			slices.SortFunc(want, func(a, b vectorSymbol) int { return strings.Compare(a.ID, b.ID) })
			if !slices.Equal(got, want) {
				out, err := json.MarshalIndent(got, "", "  ")
				t.Fatalf("vector %d: the writer reads (%v)\n%s", i, err, out)
			}
		})
	}
}

// vectorSymbols projects the records onto what a vector pins, by id.
func vectorSymbols(t *testing.T, syms []apiSymbol) []vectorSymbol {
	t.Helper()
	out := make([]vectorSymbol, 0, len(syms))
	for _, s := range syms {
		out = append(out, vectorSymbol{ID: s.ID, Kind: s.Kind, Spelled: s.Spelled, Canonical: s.Canonical, Owner: s.Owner})
	}
	slices.SortFunc(out, func(a, b vectorSymbol) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// readUniverse checks a universe of packages given as sources, dependencies
// first, and reads every exported symbol of every one of them as the writer
// does. Sources import nothing outside the universe.
func readUniverse(t *testing.T, pkgs []vectorPackage) []apiSymbol {
	t.Helper()
	fset := token.NewFileSet()
	checked := map[string]*types.Package{}
	codes := map[string]*apiCode{}
	var out []apiSymbol
	for _, p := range pkgs {
		files := parseVectorFiles(t, fset, p)
		info := &types.Info{
			Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{},
			Implicits: map[ast.Node]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{},
		}
		conf := types.Config{Importer: cellImporter{checked: checked}, IgnoreFuncBodies: true}
		tp, err := conf.Check(p.Path, fset, files, info)
		if err != nil {
			t.Fatalf("%s does not type-check: %v", p.Path, err)
		}
		checked[p.Path] = tp
		r := &pkgReader{pkg: tp, info: info, files: files, modDir: "/vectors", place: placeOf(p.Path), codes: codes}
		out = append(out, r.readSymbols(fset)...)
	}
	return out
}

// parseVectorFiles parses one package of a universe, its files by name.
func parseVectorFiles(t *testing.T, fset *token.FileSet, p vectorPackage) []*ast.File {
	t.Helper()
	names := slices.Sorted(maps.Keys(p.Files))
	files := make([]*ast.File, 0, len(names))
	for _, name := range names {
		f, err := parser.ParseFile(fset, path.Join("/vectors", p.Path, name), p.Files[name], parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s/%s: %v", p.Path, name, err)
		}
		files = append(files, f)
	}
	return files
}
