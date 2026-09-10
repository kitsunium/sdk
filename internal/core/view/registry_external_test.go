package view_test

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	coreview "github.com/kitsunium/sdk/internal/core/view"
)

// fakeFactory is a Factory double. Each instance is distinct, which is what
// makes the duplicate-registration test meaningful.
type fakeFactory struct {
	// name is the Engine key this double claims.
	name coreview.Engine
	// tag distinguishes two doubles that claim the same name.
	tag string
}

// Engine returns the registry key this double claims.
func (f fakeFactory) Engine() coreview.Engine { return f.name }

// New returns a stub Renderer; nothing here parses anything.
func (f fakeFactory) New(_ coreview.Config) (coreview.Renderer, error) {
	return stubRenderer{}, nil
}

// TestRegisterRefusesANilFactoryAtBoot pins the panic. A broken registration
// is a programming error that must be visible at import, not a value threaded
// through a call chain nobody is executing yet.
func TestRegisterRefusesANilFactoryAtBoot(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("Register(nil) did not panic")
		}
	}()
	coreview.Register(nil)
}

// TestRegisterRefusesTheEmptyEngineName pins the other boot-time refusal.
// Engine("") is what an uninitialised variable holds.
func TestRegisterRefusesTheEmptyEngineName(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("Register with an empty Engine name did not panic")
		}
	}()
	coreview.Register(fakeFactory{name: "", tag: "empty"})
}

// TestRegisteringTheSameFactoryTwiceIsANoOp separates idempotence from
// conflict: re-running an import is not an error.
func TestRegisteringTheSameFactoryTwiceIsANoOp(t *testing.T) {
	factory := fakeFactory{name: "idempotent", tag: "one"}
	coreview.Register(factory)
	coreview.Register(factory)
	found, ok := coreview.Lookup("idempotent")
	if !ok {
		t.Fatal("the factory is not registered after two identical Registers")
	}
	if found != coreview.Factory(factory) {
		t.Fatal("Lookup returned a different factory")
	}
}

// TestTwoDistinctFactoriesUnderOneNamePanic is the registry's sharpest rule.
//
// Last-write-wins in THIS registry can mean the engine that escapes was
// replaced by one that does not, at import time, with no call site to blame.
func TestTwoDistinctFactoriesUnderOneNamePanic(t *testing.T) {
	coreview.Register(fakeFactory{name: "contested", tag: "first"})
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("a second, distinct factory under one name did not panic")
		}
	}()
	coreview.Register(fakeFactory{name: "contested", tag: "second"})
}

// TestAvailableIsSortedAndComplete keeps the diagnostic deterministic.
func TestAvailableIsSortedAndComplete(t *testing.T) {
	coreview.Register(fakeFactory{name: "zeta", tag: "z"})
	coreview.Register(fakeFactory{name: "alpha", tag: "a"})
	names := coreview.Available()
	if !slices.IsSorted(names) {
		t.Fatalf("Available() = %v, not sorted", names)
	}
	for _, want := range []coreview.Engine{"alpha", "zeta"} {
		if !slices.Contains(names, want) {
			t.Fatalf("Available() = %v, missing %q", names, want)
		}
	}
}

// TestOpenRefusesAnUnclaimedNameRatherThanFallingBack is the no-default rule.
//
// A fallback here would mean a typo in a configuration file silently choosing
// how every value in the program is escaped.
func TestOpenRefusesAnUnclaimedNameRatherThanFallingBack(t *testing.T) {
	renderer, err := coreview.Open("nobody-claims-this", coreview.Config{})
	if renderer != nil {
		t.Fatal("Open returned a Renderer for an unregistered engine")
	}
	if !errors.Is(err, coreview.EngineUnknown) {
		t.Fatalf("Open error = %v, want EngineUnknown", err)
	}
}

// TestOpenDelegatesToTheFactory proves the registry adds no Config checks of
// its own — the factory owns every one of them.
func TestOpenDelegatesToTheFactory(t *testing.T) {
	coreview.Register(fakeFactory{name: "delegating", tag: "d"})
	renderer, err := coreview.Open("delegating", coreview.Config{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if renderer.ContentType() != coreview.ContentTypeHTML {
		t.Fatalf("content type = %q", renderer.ContentType())
	}
}

// TestLookupMissIsACleanMiss: an absent name is (nil, false), never an error.
func TestLookupMissIsACleanMiss(t *testing.T) {
	factory, ok := coreview.Lookup("absent")
	if ok || factory != nil {
		t.Fatalf("Lookup(absent) = (%v, %v), want (nil, false)", factory, ok)
	}
}

// TestDefaultsAreTheDocumentedOnes pins the two clamped constants, so a change
// to either shows up as a failing test rather than as a different ceiling.
func TestDefaultsAreTheDocumentedOnes(t *testing.T) {
	if coreview.DefaultMaxBytes != 8<<20 {
		t.Fatalf("DefaultMaxBytes = %d, want 8 MiB", coreview.DefaultMaxBytes)
	}
	if coreview.MaxPooledBytes != 1<<20 {
		t.Fatalf("MaxPooledBytes = %d, want 1 MiB", coreview.MaxPooledBytes)
	}
	if coreview.MaxPooledBytes >= coreview.DefaultMaxBytes {
		t.Fatal("the pool ceiling must stay below the render ceiling, or one big page pins memory forever")
	}
}

// declaredIdentifiers parses this package's non-test sources and returns every
// top-level identifier it declares.
//
// Reflection cannot enumerate a package, so a test that wants to assert the
// ABSENCE of a symbol — six trust constructors that must not exist — has to
// read the source.
func declaredIdentifiers(tb testing.TB) map[string]bool {
	tb.Helper()
	fset := token.NewFileSet()
	dir := filepath.Join(sdkRoot(tb), "internal", "core", "view")
	pkgs, err := parser.ParseDir(fset, dir, func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		tb.Fatalf("parse package source: %v", err)
	}
	names := map[string]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			collectDeclarations(file, names)
		}
	}
	if len(names) == 0 {
		tb.Fatal("no declarations parsed; the audit would pass vacuously")
	}
	return names
}

// collectDeclarations records every top-level declaration in file.
func collectDeclarations(file *ast.File, names map[string]bool) {
	for _, decl := range file.Decls {
		switch typed := decl.(type) {
		case *ast.FuncDecl:
			names[typed.Name.Name] = true
		case *ast.GenDecl:
			collectSpecNames(typed, names)
		}
	}
}

// collectSpecNames records the names bound by a const/var/type declaration.
func collectSpecNames(decl *ast.GenDecl, names map[string]bool) {
	for _, spec := range decl.Specs {
		switch typed := spec.(type) {
		case *ast.ValueSpec:
			for _, ident := range typed.Names {
				names[ident.Name] = true
			}
		case *ast.TypeSpec:
			names[typed.Name.Name] = true
		}
	}
}

// sdkRoot resolves the repository root. Under Bazel the sandbox strips the
// source-tree relationship, so the runfiles root is used when the test runner
// provides it — the same mechanism //internal/kernel/errs uses for its AST
// audits, and the reason this package's go_test carries
// data = ["//:audit_sources"].
func sdkRoot(tb testing.TB) string {
	tb.Helper()
	if srcdir, workspace := os.Getenv("TEST_SRCDIR"), os.Getenv("TEST_WORKSPACE"); srcdir != "" && workspace != "" {
		bazelRoot := filepath.Join(srcdir, workspace)
		if _, err := os.Stat(filepath.Join(bazelRoot, "go.work")); err == nil {
			return bazelRoot
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		tb.Fatalf("cwd lookup failed: %v", err)
	}
	for dir := cwd; dir != "/" && dir != ""; dir = filepath.Dir(dir) {
		if _, statErr := os.Stat(filepath.Join(dir, "go.work")); statErr == nil {
			return dir
		}
	}
	tb.Fatalf("go.work not found above %s; this audit needs the workspace layout", cwd)
	return ""
}
