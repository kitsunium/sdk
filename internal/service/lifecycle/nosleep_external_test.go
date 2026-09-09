package lifecycle_test

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
// suspends on the real clock, so a single call anywhere in this package would
// make the shutdown budget untestable in the one way that matters: a budget
// assertion would become a sleep, a sleep would become a tolerance, and a
// tolerance would become a flake somebody eventually deletes — taking the
// guard on "an expired budget does not cut short what could finish" with it.
//
// Package time's READING and VALUE surface — time.Time, time.Duration,
// time.Second — is not listed and is used freely: a budget is a duration and a
// transition carries instants.
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

// TestPackageNeverWaitsOnTheWallClock is the executable form of this domain's
// central testing claim, and it covers the production sources AND the suite.
//
// It is worth enforcing rather than asserting because the temptation is
// specific and strong here: every timing case in this package is "wait for a
// budget to expire", and `time.Sleep(budget)` is the shortest thing to type.
// A comment saying "do not sleep here" would not survive the first contributor
// in a hurry; a named test does.
//
// It reads the sources through the AST rather than by grepping, so the very
// identifiers it forbids can be written in this file's own prose.
func TestPackageNeverWaitsOnTheWallClock(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(sdkRoot(t), "internal", "service", "lifecycle")
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
// claims to catch — the same reason internal/kernel/errs runs its registry
// audits against fixtures rather than trusting that they would fire.
func TestWallClockAuditDetectsAViolation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		want   int
	}{
		{
			name: "a sleeping test is caught",
			source: `package fixture

import "time"

func slow() { time.Sleep(time.Second) }
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
			name: "reading and building durations is not a wait",
			source: `package fixture

import "time"

func budget() time.Duration { return 30 * time.Second }
`,
			want: 0,
		},
		{
			name: "a NewTimer on an injected clock is not a wall-clock wait",
			source: `package fixture

type waiter interface{ NewTimer(int) any }

func armed(clk waiter) any { return clk.NewTimer(1) }
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
