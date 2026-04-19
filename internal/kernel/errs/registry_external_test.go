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

// sdkRoot resolves the repository root by walking up from the test's CWD
// until a go.work file is found. Fails (not skips) if the workspace layout
// is missing, since audits must always run against a real tree.
func sdkRoot(tb testing.TB) (root string) {
	tb.Helper()
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

// collectDefineCalls walks every non-test .go file under root/internal and
// root/pkg, returning each (file:line, call) pair whose callee resolves to
// errs.Define. Used by the audits below.
func collectDefineCalls(tb testing.TB, root string) (out []defineCall) {
	tb.Helper()
	fset := token.NewFileSet()
	for _, sub := range []string{"internal", "pkg"} {
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
