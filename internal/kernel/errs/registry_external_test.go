package errs_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// sdkRoot resolves the repository root. Under `go test` it walks up from
// the test's CWD until a go.work file is found. Under `bazel test` it
// reads TEST_SRCDIR + TEST_WORKSPACE (runfiles root) since the sandbox
// strips the CWD relationship with the source tree. The Bazel test target
// ships //:audit_sources as runfiles so go.work and every *.go file live
// under that root.
//
// Fails (not skips) if neither path resolves, because audits must always
// run against a real tree.
func sdkRoot(tb testing.TB) (root string) {
	tb.Helper()
	//: Bazel test runner sets TEST_SRCDIR + TEST_WORKSPACE; prefer them
	//: when present so the audit works inside bazel's sandbox.
	if srcdir := os.Getenv("TEST_SRCDIR"); srcdir != "" {
		if wks := os.Getenv("TEST_WORKSPACE"); wks != "" {
			bazelRoot := filepath.Join(srcdir, wks)
			if _, err := os.Stat(filepath.Join(bazelRoot, "go.work")); err == nil {
				return bazelRoot
			}
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		tb.Fatalf("cwd lookup failed: %v", err)
	}
	for dir := cwd; dir != "/" && dir != ""; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
	}
	tb.Fatalf("go.work not found above %s; audit requires the workspace layout", cwd)
	return ""
}

// collectDefineCalls walks every non-test.go file under root/internal,
// root/pkg, and root/third-party, returning each (file:line, call) pair whose
// callee resolves to errs.Define. third-party is included so the opt-in
// vendor-dependent emitters (e.g. the AWS writers, ADR 0012) are audited for
// the same Public-is-literal / reason / code-uniqueness invariants as the rest
// of the SDK. Used by the audits below.
func collectDefineCalls(tb testing.TB, root string) (out []defineCall) {
	tb.Helper()
	fset := token.NewFileSet()
	for _, sub := range []string{"internal", "pkg", "third-party"} {
		base := filepath.Join(root, sub)
		filepath.Walk(base, func(path string, info os.FileInfo, _ error) error { //nolint:errcheck // audit is best-effort
			if info == nil || info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				return nil
			}
			ast.Inspect(file, func(n ast.Node) bool {
				spec, ok := n.(*ast.ValueSpec)
				if !ok || len(spec.Names) == 0 || len(spec.Values) == 0 {
					return true
				}
				for i, val := range spec.Values {
					call, ok := val.(*ast.CallExpr)
					if !ok {
						continue
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "Define" {
						continue
					}
					ident, ok := sel.X.(*ast.Ident)
					if !ok || ident.Name != "errs" {
						continue
					}
					out = append(out, defineCall{
						varName:  spec.Names[i].Name,
						call:     call,
						pos:      fset.Position(call.Pos()),
						filePath: path,
					})
				}
				return true
			})
			return nil
		})
	}
	return out
}

type defineCall struct {
	varName  string
	call     *ast.CallExpr
	pos      token.Position
	filePath string
}

func TestAuditPublicIsStringLiteral(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"every errs.Define public argument is a string literal"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := sdkRoot(t)
			for _, dc := range collectDefineCalls(t, root) {
				if len(dc.call.Args) < 3 {
					t.Errorf("%s: Define call has %d args", dc.pos, len(dc.call.Args))
					continue
				}
				lit, ok := dc.call.Args[2].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Errorf("%s: public must be a string literal, got %T", dc.pos, dc.call.Args[2])
				}
			}
		})
	}
}

func TestAuditReasonMatchesVarName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"every errs.Define reason equals screamingSnake(varName)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := sdkRoot(t)
			for _, dc := range collectDefineCalls(t, root) {
				if len(dc.call.Args) < 2 {
					continue
				}
				reasonLit, ok := dc.call.Args[1].(*ast.BasicLit)
				if !ok || reasonLit.Kind != token.STRING {
					continue
				}
				reason := strings.Trim(reasonLit.Value, `"`)
				want := camelToScreamingSnake(dc.varName)
				if reason != want {
					t.Errorf("%s: var %q reason %q, want %q",
						dc.pos, dc.varName, reason, want)
				}
			}
		})
	}
}

func TestAuditCodeUniqueness(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"no two errs.Define calls share a code"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := sdkRoot(t)
			seen := map[string]string{}
			for _, dc := range collectDefineCalls(t, root) {
				if len(dc.call.Args) < 1 {
					continue
				}
				ident, ok := dc.call.Args[0].(*ast.Ident)
				if !ok {
					continue
				}
				key := ident.Name
				if prev, exists := seen[key]; exists {
					t.Errorf("%s: duplicate code identifier %s (also used by %s)",
						dc.pos, key, prev)
				} else {
					seen[key] = dc.varName
				}
			}
		})
	}
}

func TestAuditScansThirdParty(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		code string
	}
	tests := []tc{
		//: a known errs.Define code from a third-party/* emitter (ADR 0012 AWS
		//: writers) MUST be collected — proves the audit walks third-party, not
		//: just internal/ + pkg/. Guards against a regression of the scan roots.
		{"s3 writer sentinel is audited", "CodeS3ClientInitFailed"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root := sdkRoot(t)
		found := false
		for _, dc := range collectDefineCalls(t, root) {
			if len(dc.call.Args) == 0 {
				continue
			}
			//: arg[0] is the dotted-quad code identifier passed to errs.Define.
			if ident, ok := dc.call.Args[0].(*ast.Ident); ok && ident.Name == c.code {
				found = true
				break
			}
		}
		//: absence means the audit is blind to third-party — the ADR-0012 drift.
		if !found {
			t.Errorf("%s: %q not collected; audit does not scan third-party", c.name, c.code)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// camelToScreamingSnake converts a Go identifier (e.g. WriterNil) to its
// SCREAMING_SNAKE equivalent (WRITER_NIL). Handles consecutive uppercase
// segments (HTTPError → HTTP_ERROR).
func camelToScreamingSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			prev := rune(s[i-1])
			// Insert underscore when transitioning from lowercase/digit to
			// uppercase, or between an uppercase run and a lowercase start.
			isPrevLower := prev >= 'a' && prev <= 'z'
			isPrevDigit := prev >= '0' && prev <= '9'
			if isPrevLower || isPrevDigit {
				b.WriteByte('_')
			} else if i+1 < len(s) {
				next := rune(s[i+1])
				if next >= 'a' && next <= 'z' && prev >= 'A' && prev <= 'Z' {
					b.WriteByte('_')
				}
			}
		}
		if r >= 'a' && r <= 'z' {
			b.WriteRune(r - 32)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestRegistryMarker(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{
		{"marker is non-empty", "sdk-registry-audit-v1"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := errs.RegistryMarker(); got != c.want {
			t.Errorf("RegistryMarker() = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
