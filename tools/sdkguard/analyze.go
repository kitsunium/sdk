// Command sdkguard — source scanning, import resolution and suppressions.
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
)

// suppressPrefix introduces an inline exemption. The rule ID and a reason must
// follow it on the same line.
const suppressPrefix string = "//sdkguard:allow"

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
	// suppressed records, per line, which rule IDs an inline directive exempts.
	suppressed map[int]map[string]bool
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
	ctx := &fileCtx{
		fset:       fset,
		byPath:     importsOf(file),
		suppressed: suppressionsOf(fset, file),
	}

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

// importsOf maps each imported path to the local name it is bound to.
func importsOf(file *ast.File) map[string]string {
	out := make(map[string]string, len(file.Imports))
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
		// A blank import binds nothing callable, so no selector can reference
		// it and no rule can fire on it.
		if name == "_" {
			continue
		}
		out[path] = name
	}
	return out
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
	return ok && ident.Name == local
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
