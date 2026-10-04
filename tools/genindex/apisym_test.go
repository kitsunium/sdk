// Package main — what the writer reads of a package beyond its signatures:
// error codes and sentinels, re-exports, fields and docs, layers and
// families.
package main

import (
	"slices"
	"testing"
)

// fakeErrs is the part of the SDK's errs package a sentinel needs: Code and
// Define, at their real import path.
const fakeErrs string = `package errs

// Code is a dotted-quad error code.
type Code uint32

// Error is a sentinel.
type Error struct{ code Code }

// DefineOption adjusts a sentinel.
type DefineOption func(*Error)

// Define makes a sentinel.
func Define(code Code, reason, public, private string, opts ...DefineOption) *Error { return nil }
`

// symbolByID finds a record by id, failing the test when it is absent.
func symbolByID(t *testing.T, syms []apiSymbol, id string) apiSymbol {
	t.Helper()
	i := slices.IndexFunc(syms, func(s apiSymbol) bool { return s.ID == id })
	if i < 0 {
		t.Fatalf("no record %s", id)
	}
	return syms[i]
}

// Test_readSymbols_codes pins the codes: an errs.Code constant carries its
// dotted quad; a variable initialised with errs.Define — under any import
// name, parenthesised or not — carries the sentinel's code, reason and public
// text; a variable initialised with a sentinel inherits its code through any
// chain of re-exports, within a package or across packages, and names it in
// init; an unexported sentinel is no record.
func Test_readSymbols_codes(t *testing.T) {
	t.Parallel()
	syms := readUniverse(t, []vectorPackage{
		{Path: errsPath, Files: map[string]string{"errs.go": fakeErrs}},
		{Path: "example.com/core", Files: map[string]string{"codes.go": `package core

import "github.com/kitsunium/sdk/internal/kernel/errs"

// The codes of core.
const (
	// CodeA is a.
	CodeA errs.Code = 0x00_02_15_01
	CodeB errs.Code = 0x00_03_33_0a
)

var (
	// B re-exports A, declared after it.
	B = A
	// A is a sentinel.
	A = errs.Define(CodeA, "A_FAILED", "a failed", "private")
	hidden = errs.Define(CodeB, "HIDDEN", "hidden", "private")
)
`}},
		{Path: "example.com/facade", Files: map[string]string{"facade.go": `package facade

import (
	core "example.com/core"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// CodeA re-exports a code.
const CodeA = core.CodeA

var (
	// A re-exports a sentinel.
	A = core.A
	// C re-exports a re-export.
	C = core.B
	// D is a sentinel of its own, under another import name.
	D = (kerrs.Define(core.CodeB, "D_FAILED", "d failed", "private"))
	// E is parenthesised.
	E = (core.A)
	// F is no sentinel.
	F = 3
)
`}},
	})
	type tc struct {
		// id is the record.
		id string
		// want is its code, or nil.
		want *apiCode
		// init is its init.
		init string
	}
	a := &apiCode{Value: "0.2.21.1", Reason: "A_FAILED", Public: "a failed"}
	tests := []tc{
		{id: "go:example.com/core.CodeA", want: &apiCode{Value: "0.2.21.1"}},
		{id: "go:example.com/core.CodeB", want: &apiCode{Value: "0.3.51.10"}},
		{id: "go:example.com/core.A", want: a},
		{id: "go:example.com/core.B", want: a, init: "go:example.com/core.A"},
		{id: "go:example.com/facade.CodeA", want: &apiCode{Value: "0.2.21.1"}, init: "go:example.com/core.CodeA"},
		{id: "go:example.com/facade.A", want: a, init: "go:example.com/core.A"},
		{id: "go:example.com/facade.C", want: a, init: "go:example.com/core.B"},
		{id: "go:example.com/facade.D", want: &apiCode{Value: "0.3.51.10", Reason: "D_FAILED", Public: "d failed"}},
		{id: "go:example.com/facade.E", want: a, init: "go:example.com/core.A"},
		{id: "go:example.com/facade.F"},
	}
	for _, c := range tests {
		t.Run(c.id, func(t *testing.T) {
			t.Parallel()
			s := symbolByID(t, syms, c.id)
			if (s.Code == nil) != (c.want == nil) || s.Code != nil && *s.Code != *c.want {
				t.Fatalf("code = %+v, want %+v", s.Code, c.want)
			}
			if s.Init != c.init {
				t.Fatalf("init = %q, want %q", s.Init, c.init)
			}
		})
	}
	if slices.ContainsFunc(syms, func(s apiSymbol) bool { return s.Name == "hidden" }) {
		t.Fatal("an unexported sentinel was recorded")
	}
}

// Test_readSymbols_docs pins the docs: a spec's own comment, else its
// group's; a field's doc comment, else its line comment; an interface
// method's comment; a constant's exact value.
func Test_readSymbols_docs(t *testing.T) {
	t.Parallel()
	syms := readUniverse(t, []vectorPackage{{Path: "example.com/d", Files: map[string]string{"d.go": `package d

// Sizes are documented as a group.
const (
	Small = 1
	// Large has its own.
	Large = 2
	Name  = "n"
)

// Config is configured.
type Config struct {
	// Size is documented above.
	Size int
	Mode string // Mode is documented on its line.
	plain bool
}

// Doer does.
type Doer interface {
	Do() // Do is documented on its line.
}
`}}})
	if got := symbolByID(t, syms, "go:example.com/d.Small").Doc; got != "Sizes are documented as a group.\n" {
		t.Fatalf("a spec with no comment of its own: doc = %q, want the group's", got)
	}
	if got := symbolByID(t, syms, "go:example.com/d.Large").Doc; got != "Large has its own.\n" {
		t.Fatalf("a spec with its own comment: doc = %q", got)
	}
	if got := symbolByID(t, syms, "go:example.com/d.Name").Value; got != `"n"` {
		t.Fatalf("a string constant's value = %q, want it quoted", got)
	}
	cfg := symbolByID(t, syms, "go:example.com/d.Config")
	want := []apiField{
		{Name: "Size", Type: "int", Doc: "Size is documented above.\n"},
		{Name: "Mode", Type: "string", Doc: "Mode is documented on its line.\n"},
	}
	if !slices.Equal(cfg.Fields, want) {
		t.Fatalf("fields = %+v, want %+v", cfg.Fields, want)
	}
	if got := symbolByID(t, syms, "go:example.com/d.Doer.Do").Doc; got != "Do is documented on its line.\n" {
		t.Fatalf("an interface method's doc = %q", got)
	}
}

// Test_placeOf pins the layers and families: the layer by directory, the
// first directory below a grouped layer as the family, root for the kernel's
// and pkg/v1's root packages, none in third-party and framework, nothing
// outside every layer.
func Test_placeOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		// dir is the package's directory.
		dir string
		// want is its place.
		want pkgPlace
	}{
		{dir: "internal/kernel/errs", want: pkgPlace{layer: "kernel", family: familyRoot}},
		{dir: "internal/kernel/concur/group", want: pkgPlace{layer: "kernel", family: "concur"}},
		{dir: "internal/core/app/lock", want: pkgPlace{layer: "core", family: "app"}},
		{dir: "internal/core/net", want: pkgPlace{layer: "core", family: "net"}},
		{dir: "internal/service/observe/internal/otlp", want: pkgPlace{layer: "service", family: "observe"}},
		{dir: "pkg/v1/errs", want: pkgPlace{layer: "public", family: familyRoot}},
		{dir: "pkg/v1/data/semver", want: pkgPlace{layer: "public", family: "data"}},
		{dir: "third-party/aws/writer/s3", want: pkgPlace{layer: "thirdparty"}},
		{dir: "framework/connectors/sqlite", want: pkgPlace{layer: "framework"}},
		{dir: "frameworks/x", want: pkgPlace{}},
		{dir: "tools/genindex", want: pkgPlace{}},
	}
	for _, c := range tests {
		t.Run(c.dir, func(t *testing.T) {
			t.Parallel()
			if got := placeOf(c.dir); got != c.want {
				t.Fatalf("placeOf(%q) = %+v, want %+v", c.dir, got, c.want)
			}
		})
	}
}

// Test_dottedQuad pins the code's spelling: four decimal bytes, the most
// significant first.
func Test_dottedQuad(t *testing.T) {
	t.Parallel()
	tests := map[uint64]string{0x00_02_15_01: "0.2.21.1", 0x40_04_01_02: "64.4.1.2", 0xffffffff: "255.255.255.255", 0: "0.0.0.0"}
	for v, want := range tests {
		if got := dottedQuad(v); got != want {
			t.Fatalf("dottedQuad(%#x) = %q, want %q", v, got, want)
		}
	}
}
