package service_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// wallClockWaits are package time's waiting surface. Every wait of a unit runs
// on its injected clock (ADR 0214 §5), so none of them may appear in the
// engine's production files: a single one would make a timeout untestable on
// a ManualClock, and its test a sleep.
var wallClockWaits = []string{"Sleep", "After", "AfterFunc", "Tick", "NewTimer", "NewTicker"}

// sourceDir is the package's directory: the working directory under go test,
// the runfiles tree under Bazel (the go_test's data holds the sources).
func sourceDir(t *testing.T) string {
	t.Helper()
	if srcdir, wks := os.Getenv("TEST_SRCDIR"), os.Getenv("TEST_WORKSPACE"); srcdir != "" && wks != "" {
		dir := filepath.Join(srcdir, wks, "internal", "service", "proc", "service")
		if _, err := os.Stat(filepath.Join(dir, "actor.go")); err == nil {
			return dir
		}
	}
	if _, err := os.Stat("actor.go"); err != nil {
		t.Fatalf("the engine's sources are not reachable: %v", err)
	}
	return "."
}

// TestNoWallClockWait parses every production file of the engine and fails on
// a call of package time's waiting surface.
func TestNoWallClockWait(t *testing.T) {
	dir := sourceDir(t)
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no source in %s: %v", dir, err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, isIdent := sel.X.(*ast.Ident); isIdent && pkg.Name == "time" && slices.Contains(wallClockWaits, sel.Sel.Name) {
				t.Errorf("%s: time.%s — wait on the unit's clock", filepath.Base(path), sel.Sel.Name)
			}
			return true
		})
	}
}
