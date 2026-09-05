// Package main — source scanning, import resolution and suppressions.
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

// suppressionFields is the smallest number of fields a directive carries: the
// rule ID, and at least one word of the mandatory reason.
const suppressionFields int = 2

// nearbyLines is how far above a finding a directive may sit and still cover
// it — the finding's own line, or the one before it.
const nearbyLines int = 1

var (
	// generatedMarker matches the line Go tools write to mark machine-owned
	// source, per the convention in go/generate's documentation.
	generatedMarker = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

	// vendoredDirs names the trees whose contents are not the consumer's to fix.
	vendoredDirs = map[string]bool{
		"vendor":       true,
		"testdata":     true,
		"node_modules": true,
	}
)

// findingEntity is one rule violation, positioned for the standard diagnostic format.
type findingEntity struct {
	// File is the path the diagnostic points into.
	//
	// Kept flat rather than holding a token.Position: that struct carries an
	// Offset nothing here reads, and the four extra words pushed the finding
	// past the width at which passing it by value stops being free — which
	// matters because the sort comparator takes one by value per comparison.
	File string
	// Rule is the violated rule's ID (e.g. "SDK001").
	Rule string
	// Message states what was found and what to do instead.
	Message string
	// Line is the 1-based line the diagnostic points at.
	Line int
	// Column is the 1-based column the diagnostic points at.
	Column int
}

// scanDir walks dir and runs every rule over every Go file it finds.
func scanDir(dir string, rules []ruleEntity, withTests bool) (findings []findingEntity, err error) {
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		//: an unreadable root is a tool error the caller must see, not an
		//: empty result that would read as "no violations".
		if walkErr != nil {
			//: surface it rather than continuing on a partial tree.
			return walkErr
		}
		//: a directory decides only whether the walk descends into it.
		if entry.IsDir() {
			//: prune the trees nobody can act on.
			return skipDir(path, entry)
		}
		//: only Go source carries the constructs the rules match.
		if !strings.HasSuffix(path, ".go") {
			//: skip the file without complaint.
			return nil
		}
		//: test files are excluded by default: fixtures legitimately mint
		//: throwaway errors, and flagging them trains people to ignore the tool.
		if !withTests && strings.HasSuffix(path, "_test.go") {
			//: opt in with -tests when they should be covered.
			return nil
		}
		findings = append(findings, scanFile(path, rules)...)
		//: keep walking; a file yielding nothing is not a reason to stop.
		return nil
	})
	//: the named returns carry both halves of the answer.
	return findings, err
}

// skipDir reports whether a directory is outside the consumer's own source.
func skipDir(path string, dir fs.DirEntry) error {
	name := dir.Name()
	//: vendored and generated trees are not the consumer's conventions to
	//: keep, testdata holds deliberately broken fixtures, and a hidden
	//: directory (.git, .cache, a bazel symlink) never holds source we own.
	//: One condition, one effect — they were two guards saying the same thing.
	hidden := name != "." && path != "." && strings.HasPrefix(name, ".")
	//: either kind of directory holds nothing the consumer wrote.
	if hidden || vendoredDirs[name] {
		//: pruning here keeps the walk off trees nobody can act on.
		return filepath.SkipDir
	}
	//: an ordinary directory: keep walking into it.
	return nil
}

// scanFile parses one file and runs the rules over it.
//
// It reports no error: the two ways a file yields nothing — it does not parse,
// or no build includes it — are both silences by design, not failures. Naming
// an error the caller could never act on would only invite a check that always
// passes.
func scanFile(path string, rules []ruleEntity) []findingEntity {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	//: a file that does not parse is the compiler's problem to report, not
	//: ours; staying silent keeps sdkguard usable mid-refactor.
	if err != nil {
		//: no findings, and no complaint the caller would have to swallow.
		return nil
	}
	byPath, dotImports := importsOf(file)
	//: a file no build includes, or one a generator owns, is not the
	//: consumer's to fix — reporting it is noise they cannot action.
	if skipFile(file) {
		//: same silence, different reason.
		return nil
	}

	ctx := &fileCtx{
		fset:       fset,
		byPath:     byPath,
		dotImports: dotImports,
		suppressed: suppressionsOf(fset, file),
	}
	//: both resolve names the rules consult, so they run before any rule does.
	ctx.bridgeBound = ctx.bridgeBoundNames(file)
	ctx.shadowed = shadowedNames(file, byPath)

	var out []findingEntity
	//: every selected rule sees the same parsed file and context.
	for _, rule := range rules {
		//: a rule may report several times in one file.
		for _, finding := range rule.Check(ctx, file) {
			//: a justified exemption removes the finding, silently by design.
			if ctx.isSuppressed(&finding) {
				//: the directive carried a reason, so honour it.
				continue
			}
			out = append(out, finding)
		}
	}
	//: everything this file has to report.
	return out
}

// bridgeBoundNames collects the identifiers assigned from a slog-bridge
// constructor anywhere in the file.
//
// Scope is deliberately ignored: this widens what counts as sanctioned, so the
// failure mode is a missed finding rather than a false one — the right
// direction for a rule whose whole value rests on not crying wolf.
func (fc *fileCtx) bridgeBoundNames(file *ast.File) map[string]bool {
	//: a handful of bindings at most; the hint avoids the first growth.
	out := make(map[string]bool, len(fc.byPath))
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		//: only an assignment can bind a handler to a name.
		if !ok {
			//: keep walking; the binding may sit deeper.
			return true
		}
		//: a multi-value assignment puts the handler on one side of the pair.
		for _, rhs := range assign.Rhs {
			//: either bridge constructor yields a handler that forwards to the
			//: SDK Logger, so a name bound from one is sanctioned.
			if !fc.isCall(rhs, bridgeConstructorPath, "NewHandler") &&
				!fc.isCall(rhs, bridgeConstructorPath, "New") {
				//: an ordinary assignment says nothing about the bridge.
				continue
			}
			//: bind every left-hand name; the error half of the pair is
			//: harmless here because no rule matches a bare error identifier.
			for _, lhs := range assign.Lhs {
				//: only an identifier can carry the binding forward.
				if ident, isIdent := lhs.(*ast.Ident); isIdent {
					out[ident.Name] = true
				}
			}
		}
		//: walk the whole file: the binding may precede or follow the use.
		return true
	})
	//: the names slog.New may legitimately wrap in this file.
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
	//: index the import names so the walk below is a lookup, not a scan.
	for _, name := range byPath {
		locals[name] = true
	}
	//: at most every imported name can end up shadowed, never more.
	out := make(map[string]bool, len(locals))
	mark := func(expr ast.Expr) {
		//: only an identifier can shadow a package name.
		if ident, ok := expr.(*ast.Ident); ok && locals[ident.Name] {
			//: record it so every rule watching that path stops matching.
			out[ident.Name] = true
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		//: collect from every construct that binds a name.
		markDeclared(n, mark)
		//: walk the whole file: a shadow anywhere disables the package here.
		return true
	})
	//: the set of import names this file also declares.
	return out
}

// markDeclared calls mark for each identifier n binds.
//
// Split out of shadowedNames so the traversal and the grammar stay separate;
// together they carried a branch count no reader tracks at once.
func markDeclared(n ast.Node, mark func(ast.Expr)) {
	//: the two halves are split so neither carries the whole grammar: one
	//: knows the constructs binding a LIST of names, the other those binding
	//: a single identifier.
	markNameLists(n, mark)
	markSingleNames(n, mark)
}

// markNameLists handles the constructs binding several names at once.
func markNameLists(n ast.Node, mark func(ast.Expr)) {
	//: three constructs bind a list of names; everything else binds one or none.
	switch decl := n.(type) {
	//: short variable declarations: the commonest way a name is bound.
	case *ast.AssignStmt:
		//: only := binds; = assigns to something already named.
		if decl.Tok == token.DEFINE {
			//: a := may bind several names at once.
			for _, lhs := range decl.Lhs {
				mark(lhs)
			}
		}
	//: var and const blocks.
	case *ast.ValueSpec:
		//: one spec can declare several names sharing a type.
		for _, name := range decl.Names {
			mark(name)
		}
	//: parameters, named results and struct fields.
	case *ast.Field:
		//: one field can name several identifiers of the same type.
		for _, name := range decl.Names {
			mark(name)
		}
	}
}

// markSingleNames handles the constructs binding one identifier.
func markSingleNames(n ast.Node, mark func(ast.Expr)) {
	//: three constructs bind exactly one identifier apiece.
	switch decl := n.(type) {
	//: a type declaration binds its name in the file scope.
	case *ast.TypeSpec:
		mark(decl.Name)
	//: range bindings, either of which may be absent.
	case *ast.RangeStmt:
		mark(decl.Key)
		mark(decl.Value)
	//: a function name shadows a package name just as a variable would.
	case *ast.FuncDecl:
		mark(decl.Name)
	}
}

// skipFile reports whether a parsed file is outside the consumer's control.
func skipFile(file *ast.File) bool {
	//: both markers live in the header, so the scan stops at the package clause.
	for _, group := range file.Comments {
		//: only the header matters: both markers must precede the package
		//: clause to have any meaning.
		if group.Pos() > file.Package {
			//: past the header; nothing below can be either marker.
			break
		}
		//: a group holds one comment per line, and either line may be it.
		for _, c := range group.List {
			//: a generator owns this file; the consumer cannot fix it.
			if generatedMarker.MatchString(strings.TrimSpace(c.Text)) {
				//: skip it rather than report what nobody can action.
				return true
			}
			//: a file no build includes is not part of the code under review.
			if excludedByBuildTag(c.Text) {
				//: same reasoning, different marker.
				return true
			}
		}
	}
	//: an ordinary source file the consumer owns.
	return false
}

// excludedByBuildTag reports whether a //go:build line puts the file outside
// every build.
//
// The question is narrower than "is this constraint satisfiable", which is a
// SAT problem: it is "does this file build ONLY when the ignore tag is set".
// So the expression is evaluated twice, and both answers matter — with ignore
// set it must build, without it it must not. A first attempt asked only the
// second half and called "//go:build !windows" excluded, which is wrong: that
// file builds everywhere except one platform.
func excludedByBuildTag(text string) bool {
	expr, err := constraint.Parse(text)
	//: not a build line at all; ordinary comments land here.
	if err != nil {
		//: an ordinary comment excludes nothing.
		return false
	}
	//: with every tag set, including ignore.
	withIgnore := expr.Eval(func(string) bool { return true })
	//: the same, except ignore is not set.
	withoutIgnore := expr.Eval(func(tag string) bool { return tag != ignoreTag })
	//: excluded only when ignore is precisely what makes it buildable —
	//: which is what "//go:build ignore" means and nothing else does.
	return withIgnore && !withoutIgnore
}

// importsOf maps each imported path to the local name it is bound to, and
// separately records the dot imports no selector can reach.
func importsOf(file *ast.File) (map[string]string, map[string]token.Pos) {
	out := make(map[string]string, len(file.Imports))
	//: dot imports are rare; the map usually stays empty.
	dots := make(map[string]token.Pos, len(file.Imports))
	//: one pass over the import block resolves every name a rule may match.
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		//: an unquotable path cannot be matched against anything.
		if err != nil {
			//: skip it; the compiler will have more to say than we would.
			continue
		}
		//: an explicit alias wins; otherwise Go binds the path's last element,
		//: which is right for every package the rules care about.
		name := path[strings.LastIndex(path, "/")+1:]
		//: an alias overrides the element Go would have bound.
		if spec.Name != nil {
			//: the alias is what selectors in this file will spell.
			name = spec.Name.Name
		}
		//: two bindings are not names a selector can carry.
		switch name {
		//: a blank import binds nothing callable, so no selector can reference
		//: it and no rule can fire on it.
		case "_":
			//: nothing to record either way.
			continue
		//: a dot import binds no qualifier at all; the rules cannot see through
		//: it, so record it and let them report the blind spot.
		case ".":
			dots[path] = spec.Pos()
			//: recorded as a blind spot, not as a resolvable name.
			continue
		}
		out[path] = name
	}
	//: the resolvable names, and separately the ones nothing can resolve.
	return out, dots
}

// suppressionsOf collects the inline exemptions, keyed by line.
//
// A directive without a reason is ignored on purpose: an exemption nobody had
// to justify is the kind that outlives the reason it was granted for, which is
// exactly the failure mode the SDK's own .ktn-linter.yaml warns about.
func suppressionsOf(fset *token.FileSet, file *ast.File) map[int]map[string]bool {
	//: one entry per commented line at most; comments are sparse.
	out := make(map[int]map[string]bool, len(file.Comments))
	//: a directive can sit anywhere, so the whole comment set is scanned.
	for _, group := range file.Comments {
		//: a group holds one comment per line; each may be a directive.
		for _, c := range group.List {
			id, ok := parseSuppression(c.Text)
			//: an ordinary comment, or a directive with no reason.
			if !ok {
				//: only a justified directive earns an exemption.
				continue
			}
			line := fset.Position(c.Pos()).Line
			//: a directive names one rule; more on a line is unusual.
			if out[line] == nil {
				out[line] = make(map[string]bool, 1)
			}
			out[line][id] = true
		}
	}
	//: the exemptions this file grants, by line.
	return out
}

// parseSuppression extracts the rule ID from a directive that carries a reason.
func parseSuppression(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	//: an ordinary comment is not a directive.
	if !strings.HasPrefix(trimmed, suppressPrefix) {
		//: nothing to exempt.
		return "", false
	}
	fields := strings.Fields(strings.TrimPrefix(trimmed, suppressPrefix))
	//: fields[0] is the rule ID; anything after it is the mandatory reason.
	//: A bare directive does not suppress — an exemption nobody had to justify
	//: is the kind that outlives the reason it was granted for.
	if len(fields) < suppressionFields {
		//: refuse it rather than honour an unexplained exemption.
		return "", false
	}
	//: accept sdk001 as readily as SDK001; the ID is not a password.
	return strings.ToUpper(fields[0]), true
}

// isSuppressed reports whether an exemption covers this finding. A directive
// counts on the finding's own line or on the line above it, matching how Go
// developers already write //nolint and //go:build style directives.
func (fc *fileCtx) isSuppressed(finding *findingEntity) bool {
	//: a pointer because the struct is wider than the by-value threshold, and
	//: nothing here mutates it.
	for _, line := range []int{finding.Line, finding.Line - nearbyLines} {
		//: the directive covers its own line or the one below it.
		if fc.suppressed[line][finding.Rule] {
			//: a justified exemption; the finding is dropped silently.
			return true
		}
	}
	//: no directive covers this finding.
	return false
}

// isSelector reports whether n is a reference to <path>.<name>, resolving the
// local name the file bound that import to.
func (fc *fileCtx) isSelector(n ast.Expr, path, name string) bool {
	sel, ok := n.(*ast.SelectorExpr)
	//: only a selector can name a package member, and the member must match.
	if !ok || sel.Sel.Name != name {
		//: not the reference this rule watches.
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	//: the qualifier must be a plain identifier, not another expression.
	if !ok {
		//: a method on a value, not a package member.
		return false
	}
	//: the import must be present in THIS file; a selector on a same-named
	//: local variable in a file that does not import the package is not a match.
	local, ok := fc.byPath[path]
	//: the path must be imported here, under exactly this qualifier.
	if !ok || ident.Name != local {
		//: the file never bound this path to that name.
		return false
	}
	//: ...and the name must not also be bound as an identifier here, or the
	//: selector could just as well be a field access on that local.
	return !fc.shadowed[local]
}

// isCall reports whether n is a call to <path>.<name>.
func (fc *fileCtx) isCall(n ast.Node, path, name string) bool {
	call, ok := n.(*ast.CallExpr)
	//: a reference that is not called says nothing about a pipeline.
	if !ok {
		//: a bare selector is vocabulary, not construction.
		return false
	}
	//: the callee decides; the arguments are the caller's business.
	return fc.isSelector(call.Fun, path, name)
}

// imports reports whether the file imports path at all — the cheap gate a rule
// checks before walking the whole AST.
func (fc *fileCtx) imports(path string) bool {
	_, ok := fc.byPath[path]
	//: presence alone answers it; the local name is the selector's business.
	return ok
}

// at builds a findingEntity positioned on n.
func (fc *fileCtx) at(n ast.Node, rule, msg string) findingEntity {
	pos := fc.fset.Position(n.Pos())
	//: flattened here so the finding stays narrow enough to pass by value.
	return findingEntity{File: pos.Filename, Line: pos.Line, Column: pos.Column, Rule: rule, Message: msg}
}

// blindSpot reports a dot import that makes rule unable to analyse this file.
//
// Reporting beats staying silent: a rule that cannot see is not a rule that
// found nothing, and only the first is worth a clean run.
func (fc *fileCtx) blindSpot(path, rule string) []findingEntity {
	pos, dotted := fc.dotImports[path]
	//: an ordinary import resolves fine; there is no blind spot to report.
	if !dotted {
		//: the rule can see this file, so let it do its own work.
		return nil
	}
	at := fc.fset.Position(pos)
	//: point at the import itself — that is the line a reader must change.
	return []findingEntity{{
		File:   at.Filename,
		Line:   at.Line,
		Column: at.Column,
		Rule:   rule,
		Message: path + " is dot-imported, so its calls carry no qualifier and " +
			rule + " cannot analyse this file; import it normally so the rule can see it",
	}}
}
