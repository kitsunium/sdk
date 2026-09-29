package kit_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// No function of the facade names a generic alias of the facade in its
// signature. An instance of a generic alias read from export data is
// published half-built and go/types completes it from two goroutines
// (golang/go#79035): gopls and every analyzer that type-checks a product's
// packages at once would race. The signatures name the generic types of
// framework/internal/kit, whose instances do not.
func TestNoSignatureNamesAGenericAlias(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	generic := map[string]bool{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
		for spec := range typeSpecs(f) {
			if spec.Assign.IsValid() && spec.TypeParams != nil {
				generic[spec.Name.Name] = true
			}
		}
	}
	if len(generic) == 0 {
		t.Fatal("the facade declares no generic alias: the test reads the wrong files")
	}
	for _, f := range files {
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok {
				for _, m := range aliasMentions(fset, fn, generic) {
					t.Error(m)
				}
			}
		}
	}
}

// aliasMentions says where fn's signature names one of the generic aliases.
func aliasMentions(fset *token.FileSet, fn *ast.FuncDecl, generic map[string]bool) []string {
	var out []string
	ast.Inspect(fn.Type, func(n ast.Node) bool {
		// A qualified name — ikit.PortService, secret.Store — is never one
		// of the facade's aliases.
		if _, ok := n.(*ast.SelectorExpr); ok {
			return false
		}
		if id, ok := n.(*ast.Ident); ok && generic[id.Name] {
			out = append(out, fmt.Sprintf("%s: %s names the generic alias %s", fset.Position(id.Pos()), fn.Name.Name, id.Name))
		}
		return true
	})
	return out
}

// typeSpecs yields the type specifications of f.
func typeSpecs(f *ast.File) func(yield func(*ast.TypeSpec) bool) {
	return func(yield func(*ast.TypeSpec) bool) {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, s := range gd.Specs {
				if !yield(s.(*ast.TypeSpec)) {
					return
				}
			}
		}
	}
}
