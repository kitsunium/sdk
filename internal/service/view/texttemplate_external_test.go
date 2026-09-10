package view_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// bannedImport is the import that must not appear anywhere in this domain.
//
// text/template and html/template are API-compatible: swapping one import for
// the other COMPILES, passes every test in this package that does not assert
// on escaped output, and ships stored XSS. The two packages differ in exactly
// one respect — one parses the surrounding HTML to decide how to escape a
// value, and the other performs string concatenation with a syntax.
//
// A documented rule would have survived until the afternoon somebody needed to
// render an email body. This audit is the structural version of it.
const bannedImport string = "text/template"

// domainPackages are the three directories this rule covers: the port, the
// engine, and the public facade.
var domainPackages = []string{
	filepath.Join("internal", "core", "view"),
	filepath.Join("internal", "service", "view"),
	filepath.Join("pkg", "v1", "view"),
}

// TestTheDomainNeverReachesTextTemplate parses every Go file in the three view
// packages — production AND test — and fails the build on the import.
//
// Test files are included deliberately. A test that imports text/template is a
// test that can demonstrate the unescaped behaviour looks fine, which is
// exactly the argument that would precede the production import.
func TestTheDomainNeverReachesTextTemplate(t *testing.T) {
	root := sdkRoot(t)
	scanned := 0
	for _, pkg := range domainPackages {
		dir := filepath.Join(root, pkg)
		//: a package this audit cannot see is a blind spot, not a pass. pkg/v1
		//: /view is the one that could legitimately be absent mid-change, and
		//: the audit says so rather than skipping quietly.
		if _, statErr := os.Stat(dir); statErr != nil {
			t.Fatalf("%s is not readable by this audit: %v", pkg, statErr)
		}
		scanned += auditDirectory(t, pkg, dir)
	}
	if scanned == 0 {
		t.Fatal("no Go files parsed; the audit would pass vacuously")
	}
}

// auditDirectory fails the test for any Go file under dir importing the banned
// package, and returns how many files it read.
func auditDirectory(tb testing.TB, label, dir string) int {
	tb.Helper()
	count := 0
	walkErr := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		count++
		assertNoBannedImport(tb, label, path)
		return nil
	})
	if walkErr != nil {
		tb.Fatalf("walk %s: %v", label, walkErr)
	}
	return count
}

// assertNoBannedImport parses one file and fails on the banned import.
func assertNoBannedImport(tb testing.TB, label, path string) {
	tb.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	//: a file this audit cannot parse is a blind spot, not a pass.
	if err != nil {
		tb.Fatalf("parse %s: %v", path, err)
	}
	for _, imported := range file.Imports {
		unquoted, unquoteErr := strconv.Unquote(imported.Path.Value)
		if unquoteErr != nil {
			tb.Fatalf("%s: unreadable import path %s", path, imported.Path.Value)
		}
		if unquoted == bannedImport {
			tb.Fatalf("%s imports %s — the two template packages are API-compatible, so this compiles and ships stored XSS (%s)",
				path, bannedImport, label)
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
