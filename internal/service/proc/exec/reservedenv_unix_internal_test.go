//go:build unix

// Package exec — the spawn protocol's variables are listed once.
package exec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestReservedEnvKeysListed is the guard of #726: every string constant of
// this package whose value carries the spawn protocol's prefix is listed in
// reservedEnvKeys — which the trampoline strips — and has a case in
// reservedEnvCases — which proves its refusal. A variable added to the
// trampoline without both fails here, before it can reach a child unrefused
// or unstripped.
func TestReservedEnvKeysListed(t *testing.T) {
	t.Parallel()
	declared := declaredReservedKeys(t)
	if len(declared) == 0 {
		t.Fatal("no constant with the prefix was found: the source scan is broken")
	}
	listed := slices.Sorted(slices.Values(reservedEnvKeys[:]))
	cased := slices.Sorted(slices.Values(reservedEnvCases))
	if !slices.Equal(declared, listed) {
		t.Errorf("constants with prefix %q = %q, reservedEnvKeys = %q", reservedEnvPrefix, declared, listed)
	}
	if !slices.Equal(declared, cased) {
		t.Errorf("constants with prefix %q = %q, the test's cases = %q", reservedEnvPrefix, declared, cased)
	}
}

// isProductionSource reports whether entry is a production Go file.
func isProductionSource(entry os.DirEntry) bool {
	name := entry.Name()
	return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
}

// declaredReservedKeys parses the package's production files and returns,
// sorted, the value of every string constant that begins reservedEnvPrefix.
func declaredReservedKeys(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	//: a test binary run away from its sources (the guests of
	//: scripts/ci/run-test-binaries.sh) has nothing to scan; the scan runs under
	//: go test, and under Bazel through the go_test's data.
	if !slices.ContainsFunc(entries, isProductionSource) {
		t.Skip("no production source beside this test binary: the scan needs the package's directory")
	}
	fset := token.NewFileSet()
	var out []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, pErr := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if pErr != nil {
			t.Fatalf("parsing %s: %v", name, pErr)
		}
		out = append(out, prefixedConstants(file)...)
	}
	slices.Sort(out)
	return out
}

// prefixedConstants returns the string literal values of file's constants
// that begin reservedEnvPrefix and go on past it.
func prefixedConstants(file *ast.File) []string {
	var out []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			for _, value := range spec.(*ast.ValueSpec).Values {
				lit, isLit := value.(*ast.BasicLit)
				if !isLit || lit.Kind != token.STRING {
					continue
				}
				//: a variable's name: the prefix and more (the prefix itself is
				//: reservedEnvPrefix, not a variable).
				if s, uErr := strconv.Unquote(lit.Value); uErr == nil && len(s) > len(reservedEnvPrefix) && strings.HasPrefix(s, reservedEnvPrefix) {
					out = append(out, s)
				}
			}
		}
	}
	return out
}
