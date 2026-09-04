// Command sdkguard — source scanning, import resolution and suppressions.
package main

import (
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// suppressPrefix introduces an inline exemption. The rule ID and a reason must
// follow it on the same line.
const suppressPrefix string = "//sdkguard:allow"

// ignoreTag is the conventional build tag for a file that is part of no build.
const ignoreTag string = "ignore"

// generatedMarker matches the line Go tools write to mark machine-owned
// source, per the convention in go/generate's documentation.
var generatedMarker = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// Finding is one rule violation, positioned for the standard diagnostic format.
type Finding struct {
	// Pos is the file:line:col the diagnostic points at.
	Pos token.Position
	// Rule is the violated rule's ID (e.g. "SDK001").
	Rule string
	// Message states what was found and what to do instead.
	Message string
}

// fileCtx carries everything a rule needs about one parsed file: where it came
// from, which packages it imports under which local names, and which lines
// carry an exemption.
type fileCtx struct {
	// fset resolves ast positions to file:line:col.
	fset *token.FileSet
	// byPath maps an import path to the local name it is bound to in this
	// file. A rule matches selectors against the local name, so an aliased or
	// renamed import is caught exactly like a plain one.
	byPath map[string]string
	// dotImports maps a dot-imported path to the position of its import spec.
	//
	// A dot import binds no qualifier, so calls appear unqualified and no
	// selector match is possible: `. "log/slog"` turns slog.New into New, and
	// every selector-based rule goes silent on a file that may well be
	// building a second pipeline. Recording it lets the rules say so instead
	// of passing quietly, which is the only honest option without type
	// resolution.
	dotImports map[string]token.Pos
	// suppressed records, per line, which rule IDs an inline directive exempts.
	suppressed map[int]map[string]bool
	// bridgeBound names the identifiers this file assigns from a slog-bridge
	// constructor, so a handler bound to a variable is still recognised as
	// the sanctioned composition when it reaches slog.New.
	bridgeBound map[string]bool
	// shadowed names the import local names this file also declares as an
	// identifier. Without type resolution a selector cannot be told apart
	// from a field access on a same-named local, so those packages stop
	// matching here — see shadowedNames for why that direction.
	shadowed map[string]bool
}

// scanDir walks dir and runs every rule over every Go file it finds.
func scanDir(dir string, rules []Rule, withTests bool) ([]Finding, error) {
	var out []Finding
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return skipDir(path, d)
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Test files are excluded by default: fixtures legitimately mint
		// throwaway errors, and flagging them trains people to ignore the tool.
		if !withTests && strings.HasSuffix(path, "_test.go") {
			return nil
		}
		found, ferr := scanFile(path, rules)
		if ferr != nil {
			return ferr
		}
		out = append(out, found...)
		return nil
	})
	return out, err
}

// skipDir reports whether a directory is outside the consumer's own source.
func skipDir(path string, d fs.DirEntry) error {
	name := d.Name()
	// Vendored and generated trees are not the consumer's conventions to keep;
	// testdata holds deliberately broken fixtures.
	if name == "vendor" || name == "testdata" || name == "node_modules" {
		return filepath.SkipDir
	}
	// Hidden directories (.git, .cache, bazel symlinks) never hold source we own.
	if name != "." && strings.HasPrefix(name, ".") && path != "." {
		return filepath.SkipDir
	}
	return nil
}

// scanFile parses one file and runs the rules over it.
func scanFile(path string, rules []Rule) ([]Finding, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		// A file that does not parse is the compiler's problem to report, not
		// ours; staying silent keeps sdkguard usable mid-refactor.
		return nil, nil
	}
	byPath, dotImports := importsOf(file)
	// A file no build includes, or one a generator owns, is not the
	// consumer's to fix — reporting it is noise they cannot action.
	if skipFile(file) {
		return nil, nil
	}

	ctx := &fileCtx{
		fset:       fset,
		byPath:     byPath,
		dotImports: dotImports,
		suppressed: suppressionsOf(fset, file),
	}
	ctx.bridgeBound = ctx.bridgeBoundNames(file)
	ctx.shadowed = shadowedNames(file, byPath)

	var out []Finding
	for _, r := range rules {
		for _, f := range r.Check(ctx, file) {
			if ctx.isSuppressed(f) {
				continue
			}
			out = append(out, f)
		}
	}
	return out, nil
}

// bridgeBoundNames collects the identifiers assigned from a slog-bridge
// constructor anywhere in the file.
//
// Scope is deliberately ignored: this widens what counts as sanctioned, so the
// failure mode is a missed finding rather than a false one — the right
// direction for a rule whose whole value rests on not crying wolf.
func (fc *fileCtx) bridgeBoundNames(file *ast.File) map[string]bool {
	out := make(map[string]bool)
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, rhs := range assign.Rhs {
			if !fc.isCall(rhs, bridgeConstructorPath, "NewHandler") &&
				!fc.isCall(rhs, bridgeConstructorPath, "New") {
				continue
			}
			// Bind every left-hand name; the error half of the pair is
			// harmless here because no rule matches a bare error identifier.
			for _, lhs := range assign.Lhs {
				if ident, isIdent := lhs.(*ast.Ident); isIdent {
					out[ident.Name] = true
				}
			}
		}
		return true
	})
	return out
}

// shadowedNames collects the import local names this file also declares.
//
// `logger := newLogger(); logger.Version = "x"` is a field assignment on a
// local, not a write to the SDK's package variable — but AST alone cannot see
// the difference, and `logger` is a name consumers bind constantly. Matching
// anyway would report a violation that does not exist, and a rule that fires
// on correct code is the kind people switch off. So a shadowed package stops
// matching in that file: the cost is a missed finding where a file both
// shadows the name AND violates the rule, which is rarer than the false
// positive it prevents.
func shadowedNames(file *ast.File, byPath map[string]string) map[string]bool {
	locals := make(map[string]bool, len(byPath))
	for _, name := range byPath {
		locals[name] = true
	}
	out := make(map[string]bool)
	mark := func(e ast.Expr) {
		if ident, ok := e.(*ast.Ident); ok && locals[ident.Name] {
			out[ident.Name] = true
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch decl := n.(type) {
		// Short variable declarations: the commonest way a name is bound.
		case *ast.AssignStmt:
			if decl.Tok == token.DEFINE {
				for _, lhs := range decl.Lhs {
					mark(lhs)
				}
			}
		// var / const / type blocks.
		case *ast.ValueSpec:
			for _, name := range decl.Names {
				mark(name)
			}
		case *ast.TypeSpec:
			mark(decl.Name)
		// Parameters, named results and struct fields.
		case *ast.Field:
			for _, name := range decl.Names {
				mark(name)
			}
		// Range and type-switch bindings.
		case *ast.RangeStmt:
			mark(decl.Key)
			mark(decl.Value)
		case *ast.FuncDecl:
			mark(decl.Name)
		}
		return true
	})
	return out
}

// skipFile reports whether a parsed file is outside the consumer's control.
func skipFile(file *ast.File) bool {
	for _, group := range file.Comments {
		// Only the header matters: both markers must precede the package
		// clause to have any meaning.
		if group.Pos() > file.Package {
			break
		}
		for _, c := range group.List {
			if generatedMarker.MatchString(strings.TrimSpace(c.Text)) {
				return true
			}
			if excludedByBuildTag(c.Text) {
				return true
			}
		}
	}
	return false
}

// excludedByBuildTag reports whether a //go:build line puts the file outside
// every build.
//
// The expression is evaluated with every tag satisfied EXCEPT "ignore", so a
// platform-specific file still gets scanned — its rules apply on the platform
// it targets — while "//go:build ignore" is recognised as what it is.
func excludedByBuildTag(text string) bool {
	expr, err := constraint.Parse(text)
	if err != nil {
		// Not a build line at all; ordinary comments land here.
		return false
	}
	return !expr.Eval(func(tag string) bool { return tag != ignoreTag })
}

// importsOf maps each imported path to the local name it is bound to, and
// separately records the dot imports no selector can reach.
func importsOf(file *ast.File) (map[string]string, map[string]token.Pos) {
	out := make(map[string]string, len(file.Imports))
	dots := make(map[string]token.Pos)
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		// An explicit alias wins; otherwise Go binds the path's last element,
		// which is right for every package the rules care about.
		name := path[strings.LastIndex(path, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		switch name {
		// A blank import binds nothing callable, so no selector can reference
		// it and no rule can fire on it.
		case "_":
			continue
		// A dot import binds no qualifier at all; the rules cannot see through
		// it, so record it and let them report the blind spot.
		case ".":
			dots[path] = spec.Pos()
			continue
		}
		out[path] = name
	}
	return out, dots
}

// suppressionsOf collects the inline exemptions, keyed by line.
//
// A directive without a reason is ignored on purpose: an exemption nobody had
// to justify is the kind that outlives the reason it was granted for, which is
// exactly the failure mode the SDK's own .ktn-linter.yaml warns about.
func suppressionsOf(fset *token.FileSet, file *ast.File) map[int]map[string]bool {
	out := make(map[int]map[string]bool)
	for _, group := range file.Comments {
		for _, c := range group.List {
			id, ok := parseSuppression(c.Text)
			if !ok {
				continue
			}
			line := fset.Position(c.Pos()).Line
			if out[line] == nil {
				out[line] = make(map[string]bool)
			}
			out[line][id] = true
		}
	}
	return out
}

// parseSuppression extracts the rule ID from a directive that carries a reason.
func parseSuppression(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, suppressPrefix) {
		return "", false
	}
	fields := strings.Fields(strings.TrimPrefix(trimmed, suppressPrefix))
	// fields[0] is the rule ID; anything after it is the mandatory reason.
	if len(fields) < 2 {
		return "", false
	}
	return strings.ToUpper(fields[0]), true
}

// isSuppressed reports whether an exemption covers this finding. A directive
// counts on the finding's own line or on the line above it, matching how Go
// developers already write //nolint and //go:build style directives.
func (fc *fileCtx) isSuppressed(f Finding) bool {
	for _, line := range []int{f.Pos.Line, f.Pos.Line - 1} {
		if fc.suppressed[line][f.Rule] {
			return true
		}
	}
	return false
}

// isSelector reports whether n is a reference to <path>.<name>, resolving the
// local name the file bound that import to.
func (fc *fileCtx) isSelector(n ast.Expr, path, name string) bool {
	sel, ok := n.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	// The import must be present in THIS file; a selector on a same-named
	// local variable in a file that does not import the package is not a match.
	local, ok := fc.byPath[path]
	if !ok || ident.Name != local {
		return false
	}
	// ...and the name must not also be bound as an identifier here, or the
	// selector could just as well be a field access on that local.
	return !fc.shadowed[local]
}

// isCall reports whether n is a call to <path>.<name>.
func (fc *fileCtx) isCall(n ast.Node, path, name string) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	return fc.isSelector(call.Fun, path, name)
}

// imports reports whether the file imports path at all — the cheap gate a rule
// checks before walking the whole AST.
func (fc *fileCtx) imports(path string) bool {
	_, ok := fc.byPath[path]
	return ok
}

// at builds a Finding positioned on n.
func (fc *fileCtx) at(n ast.Node, rule, msg string) Finding {
	return Finding{Pos: fc.fset.Position(n.Pos()), Rule: rule, Message: msg}
}

// blindSpot reports a dot import that makes rule unable to analyse this file.
//
// Reporting beats staying silent: a rule that cannot see is not a rule that
// found nothing, and only the first is worth a clean run.
func (fc *fileCtx) blindSpot(path, rule string) []Finding {
	pos, dotted := fc.dotImports[path]
	if !dotted {
		return nil
	}
	return []Finding{{
		Pos:  fc.fset.Position(pos),
		Rule: rule,
		Message: path + " is dot-imported, so its calls carry no qualifier and " +
			rule + " cannot analyse this file; import it normally so the rule can see it",
	}}
}
