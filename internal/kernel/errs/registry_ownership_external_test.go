package errs_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// layerZeroOwner is the one package whose iota-numbered Layer-0 codes the
// audit exempts. Layer 0 is ENFORCED reserved for it at runtime by
// validateDefineArgs (a Define with Layer==0 outside the whitelist panics),
// which is stronger than this audit — and codeRangeOwners, deliberately, has
// no 0.0.0.* entry to judge them against.
const layerZeroOwner string = "internal/kernel/errs"

// codeRangeOwners is the authoritative MM.LL.PP -> owning package table for the
// ADR 0005 registry invariant "each package owns a PP slot".
//
// It is deliberately HAND-MAINTAINED and independent of the constants it
// audits. Deriving it from the tree (or from docs/error-codes.yaml, which is
// itself generated from those constants) would make any range squatting
// self-legitimising: the offending package would simply be recorded as the
// owner and the audit would stay green. A separate table is the only thing that
// can disagree with the code.
//
// Keys are the resolved Code value with the SS byte cleared. Values are the
// repo-relative directory of the OWNING package. Adding a range here is the
// just-in-time allocation step: do it in the same change that introduces the
// codes, and never renumber a published code to make this table fit.
var codeRangeOwners = map[uint64]string{
	0x00_01_03_00: "internal/kernel/ring",
	0x00_01_05_00: "internal/kernel/batcher",
	0x00_02_02_00: "internal/core/codec",
	0x00_02_03_00: "internal/core/writer",
	0x00_02_04_00: "internal/core/crypto",
	0x00_02_05_00: "internal/core/transform",
	0x00_02_06_00: "internal/core/proc",
	0x00_02_07_00: "internal/core/id",
	0x00_02_08_00: "internal/core/resilience",
	0x00_02_09_00: "internal/core/metrics",
	0x00_02_0A_00: "internal/core/config",
	0x00_02_0B_00: "internal/core/net",
	0x00_02_0C_00: "internal/core/scheduler",
	0x00_02_0D_00: "internal/core/token",
	0x00_02_0E_00: "internal/core/session",
	0x00_02_0F_00: "internal/core/validation",
	0x00_02_11_00: "internal/core/logger/level",
	0x00_02_12_00: "internal/core/cache",
	0x00_02_13_00: "internal/core/lifecycle",
	0x00_02_14_00: "internal/core/trace",
	0x00_02_15_00: "internal/core/lock",
	0x00_02_16_00: "internal/core/events",
	0x00_02_17_00: "internal/core/queue",
	0x00_02_1A_00: "internal/core/authz",
	0x00_02_1B_00: "internal/core/view",
	0x00_02_18_00: "internal/core/sql",
	0x00_02_19_00: "internal/core/vfs",
	0x00_02_1D_00: "internal/core/health",
	0x00_02_1E_00: "internal/core/i18n",
	0x00_02_1F_00: "internal/core/mail",
	0x00_02_20_00: "internal/core/cli",
	0x00_03_01_00: "internal/service/logger",
	0x00_03_02_00: "internal/service/codec/json",
	0x00_03_03_00: "internal/service/codec/xml",
	0x00_03_04_00: "internal/service/codec/yaml",
	0x00_03_05_00: "internal/service/codec/toml",
	0x00_03_06_00: "internal/service/codec/cbor",
	0x00_03_07_00: "internal/service/codec/msgpack",
	0x00_03_08_00: "internal/service/codec/csv",
	0x00_03_09_00: "internal/service/codec/asn1",
	0x00_03_0A_00: "internal/service/codec/pem",
	0x00_03_0B_00: "internal/service/codec/ndjson",
	0x00_03_0D_00: "internal/service/logger/sink/console",
	0x00_03_0E_00: "internal/service/logger/sink/file",
	0x00_03_0F_00: "internal/service/logger/sink/syslog",
	0x00_03_10_00: "internal/service/logger/middleware/multi",
	0x00_03_11_00: "internal/service/logger/middleware/async",
	0x00_03_12_00: "internal/service/logger/middleware/route",
	0x00_03_13_00: "internal/service/logger/middleware/failover",
	0x00_03_14_00: "internal/service/logger/middleware/sample",
	0x00_03_15_00: "internal/service/logger/middleware/recover",
	0x00_03_16_00: "internal/service/codec/tlv",
	0x00_03_17_00: "internal/service/codec/flatbuffers",
	0x00_03_18_00: "internal/service/codec/baseenc",
	0x00_03_19_00: "third-party/aws/writer/cloudwatch",
	0x00_03_1A_00: "internal/service/transform",
	0x00_03_1B_00: "internal/service/writer/rotfile",
	0x00_03_1C_00: "internal/service/logger/middleware/encwrite",
	0x00_03_1D_00: "internal/service/logger/middleware/tee",
	0x00_03_1E_00: "internal/service/writer/nettransport",
	0x00_03_1F_00: "internal/service/writer/journald",
	0x00_03_20_00: "third-party/db/writer/mysql",
	0x00_03_21_00: "third-party/db/writer/clickhouse",
	0x00_03_22_00: "third-party/db/writer/redis",
	0x00_03_23_00: "third-party/aws/writer/s3",
	0x00_03_24_00: "internal/service/codec/bson",
	0x00_03_25_00: "third-party/codec/hcl",
	0x00_03_26_00: "third-party/codec/protobuf",
	0x00_03_27_00: "internal/service/id",
	0x00_03_28_00: "internal/service/codec/form",
	0x00_03_29_00: "internal/service/codec/multipart",
	0x00_03_2A_00: "internal/service/crypto/jwk",
	0x00_03_2B_00: "internal/service/scheduler",
	0x00_03_2C_00: "internal/service/token",
	0x00_03_2D_00: "internal/service/metrics",
	0x00_03_2E_00: "internal/service/session",
	0x00_03_2F_00: "internal/service/validation",
	0x00_03_30_00: "internal/service/cache",
	0x00_03_31_00: "internal/service/lifecycle",
	0x00_03_32_00: "internal/service/trace",
	0x00_03_33_00: "internal/service/lock",
	0x00_03_34_00: "internal/service/events",
	0x00_03_35_00: "internal/service/queue",
	0x00_03_38_00: "internal/service/authz",
	0x00_03_39_00: "internal/service/view",
	0x00_03_36_00: "internal/service/sql",
	0x00_03_37_00: "internal/service/vfs",
	0x00_03_3B_00: "internal/service/health",
	0x00_03_3C_00: "internal/service/i18n",
	0x00_03_3D_00: "internal/service/mail",
	0x00_03_3E_00: "internal/service/cli",
	0x00_03_3F_00: "third-party/transform",
	0x01_01_00_00: "pkg/v1/logger",
	0x01_01_01_00: "pkg/v1/logger/slogbridge",
	0x01_02_00_00: "pkg/v1/codec",
}

// codeDecl is one resolved `const X Code = <value>` declaration, paired with
// the package that declares it.
type codeDecl struct {
	value   uint64
	prefix  uint64
	varName string
	pkg     string
	pos     string
}

// isCodeType reports whether expr names the errs Code type, accepting both the
// cross-package form (`errs.Code`, every emitter) and the in-package form
// (`Code`, kernel/errs' own meta-codes).
func isCodeType(expr ast.Expr) bool {
	switch typ := expr.(type) {
	case *ast.SelectorExpr:
		return typ.Sel != nil && typ.Sel.Name == "Code"
	case *ast.Ident:
		return typ.Name == "Code"
	}
	return false
}

// unparen strips any parentheses around expr.
func unparen(expr ast.Expr) ast.Expr {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = paren.X
	}
}

// isCodeConversion reports whether expr converts a value to the Code type —
// `errs.Code(0x…)`, the spelling that declares a code with no declared type.
func isCodeConversion(expr ast.Expr) bool {
	call, ok := unparen(expr).(*ast.CallExpr)
	return ok && len(call.Args) == 1 && isCodeType(unparen(call.Fun))
}

// isCodeNamed reports whether name follows the registry convention ADR 0020
// derives a Reason from: the word Code, then the reason's own words. Codec and
// CodecUnavailable begin with the same four letters and are not codes.
func isCodeNamed(name string) bool {
	rest, found := strings.CutPrefix(name, "Code")
	return found && (rest == "" || rest[0] < 'a' || rest[0] > 'z')
}

// mentionsIota reports whether expr references iota.
func mentionsIota(expr ast.Expr) (found bool) {
	ast.Inspect(expr, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok && ident.Name == "iota" {
			found = true
			return false
		}
		return !found
	})
	return found
}

// withIota returns expr with every iota replaced by index, the position of its
// ConstSpec in the declaration, so an iota group resolves like any other
// constant expression. It rebuilds exactly the nodes foldConst folds; an iota
// anywhere else is left in place for the resolver to refuse loudly.
func withIota(expr ast.Expr, index int) ast.Expr {
	switch node := expr.(type) {
	case *ast.Ident:
		if node.Name == "iota" {
			return &ast.BasicLit{Kind: token.INT, Value: strconv.Itoa(index)}
		}
		return node
	case *ast.ParenExpr:
		return &ast.ParenExpr{X: withIota(node.X, index)}
	case *ast.BinaryExpr:
		return &ast.BinaryExpr{X: withIota(node.X, index), Op: node.Op, Y: withIota(node.Y, index)}
	case *ast.CallExpr:
		args := make([]ast.Expr, len(node.Args))
		for i, arg := range node.Args {
			args[i] = withIota(arg, index)
		}
		return &ast.CallExpr{Fun: node.Fun, Args: args}
	}
	return expr
}

// classifyCodeSpec decides what one declared name is to the audit. declared is
// the expression to resolve when the name allocates a code; complaint is set
// when the name looks like a code but is declared like nothing the audit can
// judge. Both empty: not a code, nothing to audit.
//
// A name allocates when it is typed errs.Code, or has no declared type and is
// a conversion to it, or has no declared type and aliases another constant by
// name (resolved, so it is judged by the value it carries). A cross-package
// selector is a re-export and never a definition — pkg/v1 aliasing an internal
// sentinel does not make it an owner. A Code-typed name not spelled Code* is
// type machinery (kernel/errs' CIDR masks), and an explicitly non-Code type is
// not a code whatever it is called. Everything else named like a code — an
// untyped literal, a variable with no value — is the complaint, because a
// spec skipped in silence is a squat the ownership check never sees.
func classifyCodeSpec(name string, typ, value ast.Expr) (declared ast.Expr, complaint string) {
	typed := typ != nil && isCodeType(typ)
	converted := typ == nil && value != nil && isCodeConversion(value)
	//: type machinery, or a name no convention ties to a code.
	if (typed || converted) && !strings.HasPrefix(name, "Code") || !typed && !converted && !isCodeNamed(name) {
		return nil, ""
	}
	//: a Code with no value this audit can read — a variable left at its zero.
	if value == nil {
		return nil, "declares no value the audit can read"
	}
	//: a re-export: the owner is the package the selector names. Bare, as
	//: before — a parenthesised selector stays the resolver's to refuse.
	if _, isSel := value.(*ast.SelectorExpr); isSel {
		return nil, ""
	}
	//: the declared and the converted spellings of an allocation.
	if typed || converted {
		return value, ""
	}
	//: an explicit type that is not Code: not a code, whatever its name.
	if typ != nil {
		return nil, ""
	}
	//: an untyped alias of a constant of this package: judged by its value.
	if _, isIdent := unparen(value).(*ast.Ident); isIdent {
		return value, ""
	}
	return nil, "is named like a code but declared as neither errs.Code, a conversion to it, " +
		"an alias of a constant, nor a cross-package re-export"
}

// collectGenDeclCodes resolves the code declarations of one const or var
// declaration. A ConstSpec with no expression list repeats the type and the
// expressions of the last one that had them, with iota advanced — which is how
// every name of an iota group gets a value to resolve rather than a skip.
func collectGenDeclCodes(
	gen *ast.GenDecl, fset *token.FileSet, syms map[string]ast.Expr, pkg string,
) (decls []codeDecl, unresolved []string) {
	var lastType ast.Expr
	var lastValues []ast.Expr
	for index, spec := range gen.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		typ, values := vs.Type, vs.Values
		if gen.Tok == token.CONST && len(values) == 0 {
			typ, values = lastType, lastValues
		} else {
			lastType, lastValues = typ, values
		}
		for i, name := range vs.Names {
			var value ast.Expr
			if i < len(values) {
				value = values[i]
			}
			declared, complaint := classifyCodeSpec(name.Name, typ, value)
			if complaint != "" {
				unresolved = append(unresolved, fmt.Sprintf("%s: %s %s",
					fset.Position(name.Pos()), name.Name, complaint))
				continue
			}
			if declared == nil {
				continue
			}
			usesIota := mentionsIota(declared)
			if usesIota {
				declared = withIota(declared, index)
			}
			resolved, rerr := resolveCodeValue(declared, syms, map[string]bool{})
			if rerr != nil {
				unresolved = append(unresolved, fmt.Sprintf(
					"%s: cannot resolve Code declaration %s: %v",
					fset.Position(name.Pos()), name.Name, rerr))
				continue
			}
			//: kernel/errs' own meta-codes, and only those: iota-numbered,
			//: Layer 0, in the one package Layer 0 belongs to.
			if usesIota && pkg == layerZeroOwner && (resolved>>16)&0xFF == 0 {
				continue
			}
			decls = append(decls, codeDecl{
				value:   resolved,
				prefix:  resolved &^ 0xFF,
				varName: name.Name,
				pkg:     pkg,
				pos:     fset.Position(name.Pos()).String(),
			})
		}
	}
	return decls, unresolved
}

// collectCodeDeclsInDir resolves every Code declaration in the .go files of
// dir. Ownership is keyed on DECLARATIONS, not on errs.Define call sites:
// internal/core/codec declares the whole 0.2.2.* block and never calls Define
// (it formats the code into its registry errors), so a Define-keyed audit is
// blind to a range that is very much allocated.
//
// What counts as a declaration, and what is a re-export, is classifyCodeSpec's
// to say. Anything that fails to resolve, and anything named like a code that
// cannot be classified, is reported in unresolved and MUST be treated as a
// failure, for the same fail-loud reason scanCodeValues gives.
func collectCodeDeclsInDir(dir, pkg string) (decls []codeDecl, unresolved []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	syms := codeSymbols(dir)
	fset := token.NewFileSet()
	for _, entry := range entries {
		//: subdirectories are separate packages; _test.go files declare no
		//: production code range.
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, perr := parser.ParseFile(fset, path, nil, 0)
		//: an unparseable file contributes no declarations; the build catches it.
		if perr != nil {
			continue
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			found, unres := collectGenDeclCodes(gen, fset, syms, pkg)
			decls = append(decls, found...)
			unresolved = append(unresolved, unres...)
		}
	}
	return decls, unresolved
}

// collectCodeDecls is the tree-wide counterpart, walking the same three roots
// the Define audit walks (internal, pkg, third-party).
func collectCodeDecls(tb testing.TB, root string) (decls []codeDecl, unresolved []string) {
	tb.Helper()
	seen := map[string]bool{}
	for _, sub := range []string{"internal", "pkg", "third-party"} {
		//nolint:errcheck // audit is best-effort, mirroring collectDefineCalls
		filepath.Walk(filepath.Join(root, sub), func(path string, info os.FileInfo, _ error) error {
			if info == nil || !info.IsDir() || seen[path] {
				return nil
			}
			seen[path] = true
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return nil
			}
			found, unres := collectCodeDeclsInDir(path, filepath.ToSlash(rel))
			decls = append(decls, found...)
			unresolved = append(unresolved, unres...)
			return nil
		})
	}
	return decls, unresolved
}

// formatPrefix renders a range key as its dotted-quad MM.LL.PP.* form.
func formatPrefix(prefix uint64) string {
	return fmt.Sprintf("%d.%d.%d.*", (prefix>>24)&0xFF, (prefix>>16)&0xFF, (prefix>>8)&0xFF)
}

// prefixExclusivityViolations reports every range declared by more than one
// package. Extracted so the fixture regression test exercises THIS code and not
// a second copy of the rule written inside the test.
func prefixExclusivityViolations(decls []codeDecl) (violations []string) {
	owners := map[uint64]map[string]bool{}
	for _, d := range decls {
		if owners[d.prefix] == nil {
			owners[d.prefix] = map[string]bool{}
		}
		owners[d.prefix][d.pkg] = true
	}
	prefixes := slices.Collect(maps.Keys(owners))
	//: deterministic order so a multi-violation failure reads the same twice.
	slices.Sort(prefixes)
	for _, prefix := range prefixes {
		byPkg := owners[prefix]
		//: one package per range is the invariant; more than one is squatting.
		if len(byPkg) < 2 {
			continue
		}
		names := slices.Sorted(maps.Keys(byPkg))
		violations = append(violations, fmt.Sprintf(
			"range %s is declared by %d packages (ADR 0005 grants it to one): %s",
			formatPrefix(prefix), len(byPkg), strings.Join(names, ", ")))
	}
	return violations
}

// prefixOwnershipViolations reports every declaration whose range is absent
// from table, or allocated to a different package. Extracted for the same
// reason as prefixExclusivityViolations.
func prefixOwnershipViolations(decls []codeDecl, table map[uint64]string) (violations []string) {
	reported := map[string]bool{}
	for _, d := range decls {
		owner, allocated := table[d.prefix]
		key := formatPrefix(d.prefix) + "|" + d.pkg
		//: one report per (range, package) pair keeps a 20-code package from
		//: printing the same violation twenty times.
		if reported[key] {
			continue
		}
		switch {
		case !allocated:
			reported[key] = true
			violations = append(violations, fmt.Sprintf(
				"%s: %s declares %s, a range absent from codeRangeOwners; "+
					"allocate it in the same change that introduces the codes",
				d.pos, d.pkg, formatPrefix(d.prefix)))
		case owner != d.pkg:
			reported[key] = true
			violations = append(violations, fmt.Sprintf(
				"%s: %s declares %s (%s), but that range belongs to %s",
				d.pos, d.pkg, formatPrefix(d.prefix), d.varName, owner))
		}
	}
	return violations
}

// TestAuditPrefixExclusivity enforces the half of ADR 0005 that
// TestAuditCodeUniqueness cannot see. That audit keys on the FULL resolved
// value, so two packages sharing MM.LL.PP with different SS bytes — the literal
// definition of range squatting — pass it green. Uniqueness of codes and
// ownership of ranges are different invariants; this checks the second.
func TestAuditPrefixExclusivity(t *testing.T) {
	t.Parallel()
	decls, unresolved := collectCodeDecls(t, sdkRoot(t))
	//: fail-loud first: a declaration the audit cannot evaluate is a blind spot.
	for _, u := range unresolved {
		t.Error(u)
	}
	for _, v := range prefixExclusivityViolations(decls) {
		t.Error(v)
	}
}

// TestAuditPrefixOwnership checks each declared range against codeRangeOwners.
// Exclusivity alone would still let a package quietly move into a range that is
// reserved-but-unused, which is exactly what a reservation is meant to prevent;
// only a table the audited code cannot edit closes that.
func TestAuditPrefixOwnership(t *testing.T) {
	t.Parallel()
	decls, unresolved := collectCodeDecls(t, sdkRoot(t))
	for _, u := range unresolved {
		t.Error(u)
	}
	for _, v := range prefixOwnershipViolations(decls, codeRangeOwners) {
		t.Error(v)
	}
}

// TestAuditPrefixChecksDetectViolations is the reason the two audits above are
// worth having. A green audit proves nothing until it has been shown to fail on
// the violation it claims to catch — the same lesson rule 12 records about
// tests that were excluded from every lane and therefore verified nothing.
//
// The conversion and iota cases were seen failing against the collector that
// skipped every spec without a declared type and every spec mentioning iota:
// the conversion squat printed "exclusivity: want 1 violations, got 0: []" and
// "ownership: want 1 violations, got 0: []", and each of the other four
// printed the ownership line — the squats were not reported at all.
func TestAuditPrefixChecksDetectViolations(t *testing.T) {
	t.Parallel()
	const owner = "internal/service/fixture"
	table := map[uint64]string{0x00_03_40_00: owner}
	type tc struct {
		name      string
		files     map[string]string
		wantExcl  int
		wantOwned int
	}
	tests := []tc{
		//: the squatting case TestAuditCodeUniqueness is structurally blind to:
		//: same MM.LL.PP, different SS, so no full value ever collides.
		{
			name: "same range in two packages with different SS is caught",
			files: map[string]string{
				owner: `package fixture

import "github.com/kitsunium/sdk/internal/kernel/errs"

const CodeOwned errs.Code = 0x00_03_40_01
`,
				"internal/service/squatter": `package squatter

import "github.com/kitsunium/sdk/internal/kernel/errs"

const CodeSquatted errs.Code = 0x00_03_40_02
`,
			},
			wantExcl: 1,
			//: the squatter also violates ownership; the owner itself does not.
			wantOwned: 1,
		},
		//: a package declaring inside somebody else's allocated range.
		{
			name: "range allocated to another package is caught",
			files: map[string]string{
				"internal/service/intruder": `package intruder

import "github.com/kitsunium/sdk/internal/kernel/errs"

const CodeIntruding errs.Code = 0x00_03_40_01
`,
			},
			wantExcl:  0,
			wantOwned: 1,
		},
		//: a range nobody has allocated — the reserved-but-unused hole that
		//: exclusivity alone cannot see.
		{
			name: "range absent from the table is caught",
			files: map[string]string{
				owner: `package fixture

import "github.com/kitsunium/sdk/internal/kernel/errs"

const CodeUnallocated errs.Code = 0x00_03_99_01
`,
			},
			wantExcl:  0,
			wantOwned: 1,
		},
		//: the must-not-false-positive case: many serials, one owner, plus an
		//: alias and a masked expression, all legitimate.
		{
			name: "many serials of the rightful owner are accepted",
			files: map[string]string{
				owner: `package fixture

import "github.com/kitsunium/sdk/internal/kernel/errs"

const base errs.Code = 0x00_03_40_00

const CodeOne errs.Code = 0x00_03_40_01
const CodeTwo errs.Code = 0x00_03_40_02
const CodeThree errs.Code = base | 0x03
const CodeAlias errs.Code = CodeOne
`,
			},
			wantExcl:  0,
			wantOwned: 0,
		},
		//: the conversion spelling: no declared type, so the collector used to
		//: skip the spec before reading its value — a squat in this form was
		//: invisible to both checks.
		{
			name: "a conversion-form declaration squatting another range is caught",
			files: map[string]string{
				owner: `package fixture

import "github.com/kitsunium/sdk/internal/kernel/errs"

const CodeOwned errs.Code = 0x00_03_40_01
`,
				"internal/service/converter": `package converter

import "github.com/kitsunium/sdk/internal/kernel/errs"

const CodeConverted = errs.Code(0x00_03_40_02)
`,
			},
			wantExcl:  1,
			wantOwned: 1,
		},
		//: the same spelling inside a block, next to an untyped alias of it —
		//: both allocate in the intruder's name.
		{
			name: "a conversion in a const block and its untyped alias are caught",
			files: map[string]string{
				"internal/service/intruder": `package intruder

import "github.com/kitsunium/sdk/internal/kernel/errs"

const (
	CodeBlocked = errs.Code(0x00_03_40_05)
	CodeAliased = CodeBlocked
)
`,
			},
			wantExcl:  0,
			wantOwned: 1,
		},
		//: an iota group outside kernel/errs: the continuation specs carry no
		//: value of their own, and the whole group used to be skipped on the
		//: strength of a rationale that only covers Layer 0.
		{
			name: "an iota group squatting another range is caught",
			files: map[string]string{
				"internal/service/iotasquatter": `package iotasquatter

import "github.com/kitsunium/sdk/internal/kernel/errs"

const (
	CodeFirst errs.Code = 0x00_03_40_10 + iota
	CodeSecond
	CodeThird
)
`,
			},
			wantExcl:  0,
			wantOwned: 1,
		},
		//: the one iota group the old skip was written for: kernel/errs' own
		//: meta-codes, Layer 0, which validateDefineArgs reserves at runtime.
		{
			name: "kernel/errs' own Layer-0 iota block is still exempt",
			files: map[string]string{
				"internal/kernel/errs": `package errs

type Code uint32

const (
	CodeInvalidCode Code = iota + 0x00_00_00_01
	CodeInvalidReason
	CodeInvalidPublic
)
`,
			},
			wantExcl:  0,
			wantOwned: 0,
		},
		//: the exemption is Layer 0 IN kernel/errs, not "an iota group": the
		//: same block anywhere else is a range nobody allocated.
		{
			name: "a Layer-0 iota block outside kernel/errs is caught",
			files: map[string]string{
				"internal/service/layerzero": `package layerzero

import "github.com/kitsunium/sdk/internal/kernel/errs"

const (
	CodeMeta errs.Code = iota + 0x00_00_00_01
	CodeMore
)
`,
			},
			wantExcl:  0,
			wantOwned: 1,
		},
		//: and it is Layer 0, not "anything kernel/errs writes with iota".
		{
			name: "a kernel/errs iota block outside Layer 0 is caught",
			files: map[string]string{
				"internal/kernel/errs": `package errs

type Code uint32

const (
	CodeStray Code = iota + 0x00_03_40_20
	CodeStrayer
)
`,
			},
			wantExcl:  0,
			wantOwned: 1,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root := t.TempDir()
		var decls []codeDecl
		//: deterministic package order so violation counts are stable.
		pkgs := slices.Sorted(maps.Keys(c.files))
		for _, pkg := range pkgs {
			dir := filepath.Join(root, filepath.FromSlash(pkg))
			if err := os.MkdirAll(dir, 0o750); err != nil {
				t.Fatalf("mkdir fixture %s: %v", pkg, err)
			}
			if err := os.WriteFile(filepath.Join(dir, "codes.go"), []byte(c.files[pkg]), 0o600); err != nil {
				t.Fatalf("write fixture %s: %v", pkg, err)
			}
			got, unresolved := collectCodeDeclsInDir(dir, pkg)
			//: an unresolved fixture declaration would mean the collector, not
			//: the rule, is what the case is measuring.
			if len(unresolved) != 0 {
				t.Fatalf("collector failed on fixture %s: %v", pkg, unresolved)
			}
			decls = append(decls, got...)
		}
		if got := len(prefixExclusivityViolations(decls)); got != c.wantExcl {
			t.Errorf("exclusivity: want %d violations, got %d: %v",
				c.wantExcl, got, prefixExclusivityViolations(decls))
		}
		if got := len(prefixOwnershipViolations(decls, table)); got != c.wantOwned {
			t.Errorf("ownership: want %d violations, got %d: %v",
				c.wantOwned, got, prefixOwnershipViolations(decls, table))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestAuditPrefixCollectorFailsLoudOnAnUnclassifiableCodeSpec is the other
// half of classifying the conversion spelling: a spec NAMED like a code that
// the collector can classify neither as a Code declaration nor as a
// re-export used to be skipped in silence, and a skipped declaration is a
// range squat the ownership audit never sees. It is reported as unresolved
// instead, which both real-tree audits turn into a failure. The second case
// is the guard against the rule biting names that only begin with the
// letters: Codec, CodecUnavailable, a function re-exported by selector, and a
// constant whose explicit type says it is not a code.
//
// Seen failing: against the collector that skipped them, the first case
// printed "unresolved: want 2, got 0: []"; with only the untyped-literal
// complaint silenced it printed "unresolved: want 2, got 1", naming CodeUnset.
func TestAuditPrefixCollectorFailsLoudOnAnUnclassifiableCodeSpec(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		source         string
		wantUnresolved int
		wantDecls      int
	}{
		{
			name: "an untyped constant and a valueless variable named like codes are unresolved",
			source: `package vague

import "github.com/kitsunium/sdk/internal/kernel/errs"

const CodeUntyped = 0x00_03_40_07

var CodeUnset errs.Code
`,
			wantUnresolved: 2,
			wantDecls:      0,
		},
		{
			name: "names that only begin with Code, and explicit non-codes, are left alone",
			source: `package quiet

import (
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/core/codec"
)

var Codec codec.Codec = newCodec()

var CodecUnavailable = errs.Define(0x01_02_00_01, "CODEC_UNAVAILABLE", "p", "q")

var CodeOf = errs.CodeOf

const CodeVerifierLength int = 43
`,
			wantUnresolved: 0,
			wantDecls:      0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "codes.go"), []byte(tc.source), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			decls, unresolved := collectCodeDeclsInDir(dir, "internal/service/fixture")
			if len(unresolved) != tc.wantUnresolved {
				t.Errorf("unresolved: want %d, got %d: %v", tc.wantUnresolved, len(unresolved), unresolved)
			}
			if len(decls) != tc.wantDecls {
				t.Errorf("declarations: want %d, got %d: %+v", tc.wantDecls, len(decls), decls)
			}
		})
	}
}
