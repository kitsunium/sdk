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
	"strings"
	"testing"
)

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

// isCodeTyped reports whether a ValueSpec is typed as the errs Code type,
// accepting both the cross-package form (`errs.Code`, every emitter) and the
// in-package form (`Code`, kernel/errs' own meta-codes).
func isCodeTyped(spec *ast.ValueSpec) bool {
	switch typ := spec.Type.(type) {
	case *ast.SelectorExpr:
		return typ.Sel != nil && typ.Sel.Name == "Code"
	case *ast.Ident:
		return typ.Name == "Code"
	}
	return false
}

// mentionsIota reports whether expr references iota. Such a spec is skipped:
// the only iota-based Code group in the tree is kernel/errs' Layer=0 meta-code
// block, and Layer 0 is ENFORCED reserved for that package at runtime by
// validateDefineArgs (a Define with Layer==0 outside the whitelist panics).
// That mechanism is stronger than this audit, so re-checking it here would add
// a resolver special case for no gain.
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

// collectCodeDeclsInDir resolves every Code constant declaration in the .go
// files of dir. Ownership is keyed on DECLARATIONS, not on errs.Define call
// sites: internal/core/codec declares the whole 0.2.2.* block and never calls
// Define (it formats the code into its registry errors), so a Define-keyed
// audit is blind to a range that is very much allocated.
//
// A value that is a cross-package selector (`core.CodeFoo`) is a re-export, not
// a new definition, and is skipped — pkg/v1 aliasing an internal sentinel does
// not make it an owner. Anything else that fails to resolve is reported in
// unresolved and MUST be treated as a failure, for the same fail-loud reason
// scanCodeValues gives.
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
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || vs.Type == nil || !isCodeTyped(vs) {
					continue
				}
				for i, name := range vs.Names {
					//: a spec with no RHS continues an iota group — see mentionsIota.
					if i >= len(vs.Values) {
						continue
					}
					//: a Code-typed constant that is not named Code* is type
					//: machinery, not an allocation — kernel/errs' CIDR masks
					//: (MaskByMajor … MaskExact) are Code-typed by design.
					//: Code* naming is the registry convention ADR 0020 relies
					//: on to derive a Reason from the constant identifier.
					if !strings.HasPrefix(name.Name, "Code") {
						continue
					}
					value := vs.Values[i]
					if mentionsIota(value) {
						continue
					}
					//: a cross-package selector is an alias, never a definition.
					if _, isSel := value.(*ast.SelectorExpr); isSel {
						continue
					}
					resolved, rerr := resolveCodeValue(value, syms, map[string]bool{})
					if rerr != nil {
						unresolved = append(unresolved, fmt.Sprintf(
							"%s: cannot resolve Code declaration %s: %v",
							fset.Position(name.Pos()), name.Name, rerr))
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
