package lock_test

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

// wallClockWaits are package time's WAITING surface. Every one of them
// suspends on the real clock.
//
// A lock domain is where this rule earns the most. Every claim this package
// makes is about a deadline — a lease lapses, a renewal lands in time, a
// waiter wakes when the holder's TTL passes — and the tempting way to test
// each of them is to sleep past it. That produces a suite whose meaning is its
// timing: it passes on an idle laptop, flakes on a loaded CI runner, and gets
// a tolerance bolted on, then a longer sleep, then a t.Skip. The ADR 0025
// clock exists precisely so a TTL is asserted by jumping, not by waiting.
//
// Package time's READING and VALUE surface — time.Time, time.Duration,
// time.Unix — is not listed and is used freely: a lease has a deadline, and a
// deadline needs them.
var wallClockWaits = []string{"Sleep", "After", "AfterFunc", "Tick", "NewTimer", "NewTicker"}

// sdkRoot resolves the repository root. Under Bazel the sandbox strips the
// source-tree relationship, so the runfiles root is used when the test runner
// provides it; the same mechanism //internal/kernel/errs uses for its AST
// audits, and the reason this package's go_test carries
// data = ["//:audit_sources"].
func sdkRoot(tb testing.TB) string {
	tb.Helper()
	if srcdir, wks := os.Getenv("TEST_SRCDIR"), os.Getenv("TEST_WORKSPACE"); srcdir != "" && wks != "" {
		bazelRoot := filepath.Join(srcdir, wks)
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

// wallClockCalls returns every "time.<wait>" call site in one file.
func wallClockCalls(tb testing.TB, path string) []string {
	tb.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	//: a file this audit cannot parse is a blind spot, not a pass.
	if err != nil {
		tb.Fatalf("parse %s: %v", path, err)
	}
	var found []string
	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		//: only the package-qualified form; a clock.Timed's own Sleep is a
		//: wait on the INJECTED clock and is exactly what this domain wants.
		if !ok || pkg.Name != "time" || !slices.Contains(wallClockWaits, selector.Sel.Name) {
			return true
		}
		found = append(found, fset.Position(selector.Pos()).String()+": time."+selector.Sel.Name)
		return true
	})
	return found
}

// TestPackageNeverWaitsOnTheWallClock covers the production sources AND the
// test suite, because the suite is where the temptation lives: nothing in
// memory.go wants to sleep, but every expiry test does.
//
// It reads the sources through the AST rather than by grepping, so the very
// identifiers it forbids can be written in this file's own prose.
func TestPackageNeverWaitsOnTheWallClock(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(sdkRoot(t), "internal", "service", "lock")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	scanned := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		scanned++
		for _, call := range wallClockCalls(t, filepath.Join(dir, entry.Name())) {
			t.Errorf("%s — this package waits through clock.Timed, never through package time", call)
		}
	}
	//: fail loud on an empty scan. A guard that silently inspected nothing is
	//: the exact defect CLAUDE.md rule 12 records: a lane that exists, passes,
	//: and verifies nothing.
	if scanned == 0 {
		t.Fatalf("no .go files found under %s; the audit inspected nothing", dir)
	}
}

// TestWallClockAuditDetectsAViolation is why the audit above is worth having.
// A green audit proves nothing until it has been shown to fail on the thing it
// claims to catch.
func TestWallClockAuditDetectsAViolation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		want   int
	}{
		{
			name: "a test that sleeps past a TTL is caught",
			source: `package fixture

import "time"

func expire() { time.Sleep(time.Second) }
`,
			want: 1,
		},
		{
			name: "every wall-clock wait is caught, not just Sleep",
			source: `package fixture

import "time"

func waits() {
	<-time.After(time.Second)
	<-time.Tick(time.Second)
	_ = time.NewTimer(time.Second)
	_ = time.NewTicker(time.Second)
	_ = time.AfterFunc(time.Second, func() {})
}
`,
			want: 5,
		},
		{
			name: "holding a deadline is not a wait",
			source: `package fixture

import "time"

func deadline() time.Time {
	return time.Unix(0, 0).UTC().Add(time.Minute)
}
`,
			want: 0,
		},
		{
			name: "a wait on an injected clock is not a wall-clock wait",
			source: `package fixture

type waiter interface{ NewTimer(int) }

func armed(clk waiter) { clk.NewTimer(1) }
`,
			want: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "fixture.go")
			if err := os.WriteFile(path, []byte(tc.source), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			if got := wallClockCalls(t, path); len(got) != tc.want {
				t.Errorf("found %d wall-clock waits (%v), want %d", len(got), got, tc.want)
			}
		})
	}
}
