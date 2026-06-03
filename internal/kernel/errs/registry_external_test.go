package errs_test

import (
	"fmt"
	"go/ast"
	"go/constant"
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
						pkgDir:   filepath.Dir(path),
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
	pkgDir   string
}

// codeSymbols maps every package-local constant/variable identifier declared in
// the non-test .go files of dir to its declaring AST expression. The resolver
// (resolveCodeValue) follows these bindings so a Define arg that is an alias
// (CodeC = CodeA) or an expression (base | 0x01) folds to its numeric value
// instead of being trusted by name. Built once per package directory and
// memoised by the caller.
func codeSymbols(dir string) (syms map[string]ast.Expr) {
	syms = map[string]ast.Expr{}
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	//: an unreadable dir yields an empty table — the resolver then fails loud
	//: on the first unresolved ident, surfacing the gap rather than hiding it.
	if err != nil {
		return syms
	}
	for _, entry := range entries {
		//: skip subdirs and test files: a Code constant is declared in the same
		//: production package as its Define call, never in a _test.go sibling.
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, 0)
		//: an unparseable file contributes no symbols; a genuinely needed one
		//: still trips the fail-loud resolver downstream.
		if perr != nil {
			continue
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			//: only const/var GenDecls carry the Code bindings we resolve through.
			if !ok {
				continue
			}
			collectValueSpecs(gen, syms)
		}
	}
	return syms
}

// collectValueSpecs records each (name, value-expression) binding of a const or
// var declaration into syms, so resolveCodeValue can follow identifier chains.
func collectValueSpecs(gen *ast.GenDecl, syms map[string]ast.Expr) {
	for _, spec := range gen.Specs {
		value, ok := spec.(*ast.ValueSpec)
		//: import/type specs declare no value binding — nothing to record.
		if !ok {
			continue
		}
		for i, name := range value.Names {
			//: only bindings with an explicit RHS are resolvable; iota-only
			//: const groups (no value) are intentionally left unbound so the
			//: resolver fails loud rather than guessing an iota position.
			if i < len(value.Values) {
				syms[name.Name] = value.Values[i]
			}
		}
	}
}

// resolveCodeValue folds expr to its uint64 Code value using the package-local
// symbol table syms, following identifier aliases and constant expressions. It
// returns an error — never a silent zero — for any form it cannot reduce to a
// constant (cross-package selector, iota, unresolved ident, unsupported node).
// Failing loud is the whole point of WS-0 (V1/V93/V100): a silent skip is what
// let the original name-keyed audit miss value collisions.
func resolveCodeValue(expr ast.Expr, syms map[string]ast.Expr, seen map[string]bool) (uint64, error) {
	val, err := foldConst(expr, syms, seen)
	//: propagate the structural reason (alias cycle, selector, …) verbatim.
	if err != nil {
		return 0, err
	}
	out, ok := constant.Uint64Val(val)
	//: a Code is a uint32; anything that does not fit a uint64 is malformed.
	if !ok {
		return 0, fmt.Errorf("value %s does not fit uint64", val)
	}
	return out, nil
}

// foldConst recursively evaluates expr against syms, returning its constant
// value. Supported forms mirror how Code constants are actually written:
// hex/decimal literals, package-local identifier aliases, parenthesised
// expressions, binary expressions (| & + etc.), and single-arg conversions
// such as errs.Code(0x…). Every other form is an error.
func foldConst(expr ast.Expr, syms map[string]ast.Expr, seen map[string]bool) (constant.Value, error) {
	switch node := expr.(type) {
	//: literal — the leaf case, e.g. 0x00_03_18_02.
	case *ast.BasicLit:
		return constant.MakeFromLiteral(node.Value, node.Kind, 0), nil
	//: identifier — an alias; follow it through the symbol table.
	case *ast.Ident:
		return foldIdent(node, syms, seen)
	//: parens — transparent wrapper around the real expression.
	case *ast.ParenExpr:
		return foldConst(node.X, syms, seen)
	//: binary expression — e.g. base | 0x01; fold both operands then apply.
	case *ast.BinaryExpr:
		return foldBinary(node, syms, seen)
	//: call — only a single-arg type conversion (errs.Code(x)) is constant.
	case *ast.CallExpr:
		if len(node.Args) == 1 {
			return foldConst(node.Args[0], syms, seen)
		}
		return nil, fmt.Errorf("non-conversion call expression")
	//: anything else (selector, index, …) is not a resolvable constant.
	default:
		return nil, fmt.Errorf("unsupported expression %T", expr)
	}
}

// foldIdent resolves a bare identifier to its bound value, guarding against
// alias cycles and the iota sentinel.
func foldIdent(node *ast.Ident, syms map[string]ast.Expr, seen map[string]bool) (constant.Value, error) {
	//: iota cannot be resolved without const-group position — fail loud so an
	//: iota-defined Code arg is flagged, never silently trusted.
	if node.Name == "iota" {
		return nil, fmt.Errorf("iota is not statically resolvable here")
	}
	bound, ok := syms[node.Name]
	//: an ident absent from the package table is a cross-package or undeclared
	//: reference the audit must not guess at.
	if !ok {
		return nil, fmt.Errorf("unresolved identifier %q", node.Name)
	}
	//: a repeated visit means an alias cycle (CodeA = CodeB; CodeB = CodeA).
	if seen[node.Name] {
		return nil, fmt.Errorf("alias cycle through %q", node.Name)
	}
	seen[node.Name] = true
	return foldConst(bound, syms, seen)
}

// foldBinary folds both operands of a binary expression and applies the
// operator, supporting the bitwise/arithmetic forms used to compose Codes.
func foldBinary(node *ast.BinaryExpr, syms map[string]ast.Expr, seen map[string]bool) (constant.Value, error) {
	left, err := foldConst(node.X, syms, seen)
	//: a non-constant left operand makes the whole expression non-constant.
	if err != nil {
		return nil, err
	}
	right, err := foldConst(node.Y, syms, seen)
	//: same for the right operand.
	if err != nil {
		return nil, err
	}
	return constant.BinaryOp(left, node.Op, right), nil
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

// codeBinding is one resolved Define call: the numeric Code value its first
// argument folds to, plus the diagnostics needed to name a collision.
type codeBinding struct {
	value   uint64
	varName string
	pos     string
}

// scanCodeValues resolves the first argument of every collected Define call to
// its numeric Code value, returning the bindings plus a slice of fail-loud
// errors for any argument it could not reduce to a constant. Callers MUST treat
// a non-empty unresolved slice as a test failure: a Define arg the audit cannot
// evaluate is the exact blind spot WS-0 (V1/V93/V100) closes.
func scanCodeValues(calls []defineCall) (bindings []codeBinding, unresolved []string) {
	symbolCache := map[string]map[string]ast.Expr{}
	for _, dc := range calls {
		//: a Define call with no args is malformed; other audits flag arity, so
		//: skip it here rather than double-report.
		if len(dc.call.Args) < 1 {
			continue
		}
		syms, ok := symbolCache[dc.pkgDir]
		//: build (and memoise) the package-local symbol table the first time we
		//: meet a directory; resolution is keyed on the declaring package.
		if !ok {
			syms = codeSymbols(dc.pkgDir)
			symbolCache[dc.pkgDir] = syms
		}
		value, err := resolveCodeValue(dc.call.Args[0], syms, map[string]bool{})
		//: fail loud — record the unresolved arg instead of silently dropping it.
		if err != nil {
			unresolved = append(unresolved,
				fmt.Sprintf("%s: cannot resolve Code arg of %s: %v", dc.pos, dc.varName, err))
			continue
		}
		bindings = append(bindings, codeBinding{value: value, varName: dc.varName, pos: dc.pos.String()})
	}
	return bindings, unresolved
}

// TestAuditCodeUniqueness keys uniqueness on the RESOLVED numeric Code value,
// not the constant identifier name — the WS-0 fix for findings V1/V93/V100. The
// previous audit stored seen[ident.Name], so two distinct identifiers folding to
// the same dotted-quad value (e.g. CodeBaseEncUnmarshalFailed and
// CodeS3ClientInitFailed, both 0x00_03_18_02) passed green while violating the
// ADR 0005 registry invariant. It also fails loud on any Define arg that cannot
// be resolved to a constant, because a silent skip recreates the same blind spot.
func TestAuditCodeUniqueness(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"no two errs.Define calls share a resolved code value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := sdkRoot(t)
			bindings, unresolved := scanCodeValues(collectDefineCalls(t, root))
			//: fail-loud first: an unresolved arg means the audit is blind, which
			//: is precisely the regression V1/V93/V100 documents.
			for _, u := range unresolved {
				t.Error(u)
			}
			seen := map[uint64]codeBinding{}
			for _, b := range bindings {
				prev, exists := seen[b.value]
				//: same resolved value under a different name is the value
				//: collision the name-keyed audit could never see.
				if exists {
					t.Errorf("%s: code value 0x%08x of %s collides with %s (%s)",
						b.pos, b.value, b.varName, prev.varName, prev.pos)
					continue
				}
				seen[b.value] = b
			}
		})
	}
}

// collectDefineCallsInDir is the single-directory counterpart of
// collectDefineCalls, used by the fixture regression test to scan a synthetic
// package without touching the real tree. It records each errs.Define binding
// with the package directory so the value resolver can follow local aliases.
func collectDefineCallsInDir(tb testing.TB, dir string) (out []defineCall) {
	tb.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		tb.Fatalf("read fixture dir %s: %v", dir, err)
	}
	for _, entry := range entries {
		//: skip subdirs and test files, mirroring the real collector's filter.
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			tb.Fatalf("parse fixture %s: %v", path, perr)
		}
		appendDefineCalls(file, fset, path, &out)
	}
	return out
}

// appendDefineCalls extracts every `X = errs.Define(...)` binding from file and
// appends it to out, sharing the ValueSpec shape the real collector relies on.
func appendDefineCalls(file *ast.File, fset *token.FileSet, path string, out *[]defineCall) {
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		//: only value specs bind a Define result to a sentinel name.
		if !ok || len(spec.Names) == 0 || len(spec.Values) == 0 {
			return true
		}
		for i, val := range spec.Values {
			call, ok := val.(*ast.CallExpr)
			//: the RHS must be the Define call itself.
			if !ok {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			//: match errs.Define specifically, ignoring other constructors.
			if !ok || sel.Sel.Name != "Define" {
				continue
			}
			ident, ok := sel.X.(*ast.Ident)
			//: the receiver must be the errs package selector.
			if !ok || ident.Name != "errs" {
				continue
			}
			*out = append(*out, defineCall{
				varName:  spec.Names[i].Name,
				call:     call,
				pos:      fset.Position(call.Pos()),
				filePath: path,
				pkgDir:   filepath.Dir(path),
			})
		}
		return true
	})
}

// TestAuditCodeUniquenessDetectsValueCollisions is the regression test for
// findings V1/V93/V100: the uniqueness audit must key on the resolved numeric
// Code value, not the identifier name. It writes a fixture package whose Define
// args are CodeA (literal), CodeB (a DISTINCT name with the SAME value), CodeC
// (an alias of CodeA), and CodeD (an expression base|0x01). The old name-keyed
// audit could never flag these — distinct names, and aliases/expressions it did
// not even resolve — so this test FAILS before the WS-0 fix and PASSES after.
func TestAuditCodeUniquenessDetectsValueCollisions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		fixture string
		value   uint64
		want    int
	}
	tests := []tc{
		//: fixture mirrors the plan's binary-verify block verbatim: a same-value
		//: name (CodeB), an alias (CodeC), and an expression (CodeD) all folding
		//: to one value the pre-fix name-keyed audit saw as four distinct codes.
		{
			name: "same-value name, alias, and expression all fold to one value",
			fixture: `package fixture

import "github.com/kitsunium/sdk/internal/kernel/errs"

const CodeA errs.Code = 0x00020301
const CodeB errs.Code = 0x00020301 // distinct name, same value as CodeA
const CodeC errs.Code = CodeA       // alias
const base errs.Code = 0x00020300
const CodeD errs.Code = base | 0x01 // expression -> 0x00020301

var (
	A = errs.Define(CodeA, "FIX_A", "fixture a", "fixture a private")
	B = errs.Define(CodeB, "FIX_B", "fixture b", "fixture b private")
	C = errs.Define(CodeC, "FIX_C", "fixture c", "fixture c private")
	D = errs.Define(CodeD, "FIX_D", "fixture d", "fixture d private")
)
`,
			value: 0x00020301,
			want:  4,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(c.fixture), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		bindings, unresolved := scanCodeValues(collectDefineCallsInDir(t, dir))
		//: every fixture arg — including the alias and the expression — must
		//: resolve; a leftover unresolved entry proves the resolver is name-blind.
		if len(unresolved) != 0 {
			t.Fatalf("resolver failed on fixture args (must be name-blind-free): %v", unresolved)
		}
		counts := map[uint64]int{}
		for _, b := range bindings {
			counts[b.value]++
		}
		//: all fixtures fold to one value, so the value-keyed audit must count
		//: them together — the pre-fix name-keyed audit counted four distinct
		//: codes and reported zero collisions, the regression V1/V93/V100 names.
		if got := counts[c.value]; got != c.want {
			t.Fatalf("expected %d bindings resolving to 0x%08x, got %d: %+v",
				c.want, c.value, got, bindings)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
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
