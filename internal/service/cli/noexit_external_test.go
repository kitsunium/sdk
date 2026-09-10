package cli_test

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

// processEnders are the package-qualified calls that end a Go process without
// returning to the caller. os.Exit is the one flag.ExitOnError uses; log.Fatal
// and its family call it; runtime.Goexit does not exit the process but does
// abandon the goroutine mid-call, which for a CLI's single command goroutine
// has the same effect on the caller — it never gets its error.
//
// syscall.Exit is listed for completeness rather than plausibility: it is the
// one spelling somebody reaching for "just exit here" would find that os.Exit
// does not cover.
var processEnders = map[string][]string{
	"os":      {"Exit"},
	"syscall": {"Exit"},
	"runtime": {"Goexit"},
	"log":     {"Fatal", "Fatalf", "Fatalln", "Panic", "Panicf", "Panicln"},
}

// exitOnErrorModes are package flag's two error policies that take the
// decision away from the caller. ExitOnError calls os.Exit from inside a
// library; PanicOnError is os.Exit with a corrupted stack on the way out and a
// recover somewhere it does not belong. ContinueOnError is the only one this
// domain may construct, and newFlagSet is the only place it does.
var exitOnErrorModes = []string{"ExitOnError", "PanicOnError"}

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

// forbiddenSelectors returns every package-qualified call site in one file
// that would end the process, plus every flag error policy that is not
// ContinueOnError.
func forbiddenSelectors(tb testing.TB, path string) []string {
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
		if !ok {
			return true
		}
		where := fset.Position(selector.Pos()).String()
		//: os.Exit and everything that reaches it.
		if slices.Contains(processEnders[pkg.Name], selector.Sel.Name) {
			found = append(found, where+": "+pkg.Name+"."+selector.Sel.Name)
		}
		//: flag's two policies that decide on the caller's behalf.
		if pkg.Name == "flag" && slices.Contains(exitOnErrorModes, selector.Sel.Name) {
			found = append(found, where+": flag."+selector.Sel.Name)
		}
		return true
	})
	return found
}

// TestPackageNeverEndsTheProcess is the executable form of this domain's
// central claim, and it covers the production sources AND the suite.
//
// The claim is not decoration. package flag's own answer to a bad argument is
// flag.ExitOnError — os.Exit, from inside a library — and it is the DEFAULT of
// flag.CommandLine, so the thing this domain refuses is also the thing the
// substrate does by default and the shortest thing to type. A comment saying
// "do not exit here" would not survive the first contributor in a hurry; a
// named test does.
//
// The suite is audited too, and for a specific reason: a test that ends the
// process takes the whole test binary with it, so the very first failure would
// be reported as "the package crashed" rather than as the assertion it was.
//
// It reads the sources through the AST rather than by grepping, so the very
// identifiers it forbids can be written in this file's own prose and in its
// own tables.
func TestPackageNeverEndsTheProcess(t *testing.T) {
	t.Parallel()
	dirs := [][]string{
		{"internal", "core", "cli"},
		{"internal", "service", "cli"},
		{"pkg", "v1", "cli"},
	}
	root := sdkRoot(t)
	scanned := 0
	for _, parts := range dirs {
		dir := filepath.Join(append([]string{root}, parts...)...)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
				continue
			}
			scanned++
			for _, call := range forbiddenSelectors(t, filepath.Join(dir, entry.Name())) {
				t.Errorf("%s — the cli domain returns a typed error; main is the only place that may end the process", call)
			}
		}
	}
	//: fail loud on an empty scan. A guard that silently inspected nothing is
	//: the exact defect CLAUDE.md rule 12 records: a lane that exists, passes,
	//: and verifies nothing.
	if scanned == 0 {
		t.Fatalf("no .go files found under %s; the audit inspected nothing", root)
	}
}

// TestTheExitAuditDetectsAViolation is why the audit above is worth having. A
// green audit proves nothing until it has been shown to fail on the thing it
// claims to catch — the same reason internal/kernel/errs runs its registry
// audits against fixtures rather than trusting that they would fire.
func TestTheExitAuditDetectsAViolation(t *testing.T) {
	t.Parallel()
	fixture := filepath.Join(t.TempDir(), "violation.go")
	//: assembled from parts so this file contains no literal call the audit
	//: would then find in its own source.
	source := strings.Join([]string{
		"package fixture",
		"",
		"import (",
		"\t\"flag\"",
		"\t\"os\"",
		")",
		"",
		"func bad() {",
		"\tset := flag.NewFlagSet(\"x\", flag." + "ExitOnError)",
		"\t_ = set",
		"\tos." + "Exit(1)",
		"}",
	}, "\n")
	if err := os.WriteFile(fixture, []byte(source), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	found := forbiddenSelectors(t, fixture)
	if len(found) != 2 {
		t.Fatalf("the audit found %d violations in a file with two: %v", len(found), found)
	}
}
