package codec_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// coreCodecImportPath is the package whose Register the audit follows.
const coreCodecImportPath = "github.com/kitsunium/sdk/internal/core/data/codec"

// registrationReaders are the methods Register calls on the codec it is
// handed: whatever they read must be initialised when Register runs.
var registrationReaders = []string{"Name", "MIMETypes", "Extensions"}

// TestNoCodecRegistersBeforeTheVariablesItsKeysRead is the static guard behind
// supervizio/agent#305.
//
// `var Codec = codec.Register(&xCodec{})` hands Register a value whose Name,
// MIMETypes and Extensions it calls through the Codec interface. The Go
// specification (Package initialization) orders a package variable after
// another only when its initialiser REFERS to it — directly, or through a
// function or a non-interface method it references. An interface call is no
// reference, so a package table those methods read is NOT ordered before
// Codec; declared after it, it is initialised after it. The gc compiler hides
// that by laying constant composite literals out statically, before any
// initialiser runs; a build that turns the literals into code (garble
// -literals) does not, and the agent died before main on two codecs both
// claiming the empty MIME type.
//
// No runtime test can see this under gc, so the audit reads the source: for
// every package-level variable initialised by core/data/codec.Register, every
// package variable the registered type's Name, MIMETypes and Extensions read
// must be one the initialiser itself depends on.
func TestNoCodecRegistersBeforeTheVariablesItsKeysRead(t *testing.T) {
	root := sdkRoot(t)
	var dirs []string
	for _, parent := range []string{
		filepath.Join(root, "internal", "service", "data", "codec"),
		filepath.Join(root, "third-party", "codec"),
	} {
		entries, err := os.ReadDir(parent)
		if err != nil {
			t.Fatalf("read %s: %v", parent, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(parent, e.Name()))
			}
		}
	}
	registrations := 0
	for _, dir := range dirs {
		files := parsePackage(t, dir)
		found, problems := auditRegistrationOrder(files)
		registrations += found
		rel, _ := filepath.Rel(root, dir)
		for _, p := range problems {
			t.Errorf("%s: %s", rel, p)
		}
	}
	//: a walk that found nothing proves nothing: the SDK registers well over
	//: twenty codecs (nine baseenc variants alone).
	if registrations < 20 {
		t.Fatalf("the audit saw %d codec.Register initialisers, want at least 20: the source tree did not reach the test", registrations)
	}
}

// TestRegistrationOrderAuditFlagsTheTablesDeclaredAfterTheSingleton proves the
// audit catches the exact shape every codec had before the fix, and accepts
// the two shapes that replace it.
func TestRegistrationOrderAuditFlagsTheTablesDeclaredAfterTheSingleton(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		src      string
		problems int
	}{
		{
			name: "the old shape: tables read through the interface, declared after the singleton",
			src: `package x
import "github.com/kitsunium/sdk/internal/core/data/codec"
var (
	Codec codec.Codec = codec.Register(&xCodec{})
	mimeTypes = []string{"application/x"}
	extensions = []string{".x"}
)
type xCodec struct{}
func (*xCodec) Name() string { return "x" }
func (*xCodec) MIMETypes() []string { return clone(mimeTypes) }
func (*xCodec) Extensions() []string { return clone(extensions) }
func clone(s []string) []string { return append([]string(nil), s...) }
`,
			problems: 2,
		},
		{
			name: "a table reached through a helper method is still a read",
			src: `package x
import core "github.com/kitsunium/sdk/internal/core/data/codec"
var One = core.Register(&xCodec{v: 0})
type row struct{ name string }
var rows = [...]row{{name: "x"}}
type xCodec struct{ v int }
func (c *xCodec) Name() string { return c.row().name }
func (c *xCodec) MIMETypes() []string { return nil }
func (c *xCodec) Extensions() []string { return nil }
func (c *xCodec) row() *row { return &rows[c.v] }
`,
			problems: 1,
		},
		{
			name: "the fix: literals in the methods",
			src: `package x
import "github.com/kitsunium/sdk/internal/core/data/codec"
var Codec codec.Codec = codec.Register(&xCodec{})
type xCodec struct{}
func (*xCodec) Name() string { return "x" }
func (*xCodec) MIMETypes() []string { return []string{"application/x"} }
func (*xCodec) Extensions() []string { return []string{".x"} }
`,
		},
		{
			name: "the fix: the initialiser references the table through a constructor",
			src: `package x
import "github.com/kitsunium/sdk/internal/core/data/codec"
var One = codec.Register(newX(0))
type row struct{ name string }
type xCodec struct{ v int }
func newX(v int) *xCodec { _ = rows[v]; return &xCodec{v: v} }
func (c *xCodec) Name() string { return c.row().name }
func (c *xCodec) MIMETypes() []string { return nil }
func (c *xCodec) Extensions() []string { return nil }
func (c *xCodec) row() *row { return &rows[c.v] }
var rows = [...]row{{name: "x"}}
`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "x.go", tc.src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			found, problems := auditRegistrationOrder([]*ast.File{file})
			if found != 1 {
				t.Fatalf("found %d registrations, want 1", found)
			}
			if len(problems) != tc.problems {
				t.Fatalf("problems = %q, want %d", problems, tc.problems)
			}
		})
	}
}

// parsePackage parses the non-test Go files of dir, in file-name order — the
// order go build presents them to the compiler.
func parsePackage(tb testing.TB, dir string) []*ast.File {
	tb.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		tb.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			tb.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, file)
	}
	return files
}

// varInit is one package variable's initialiser and whether it registers.
type varInit struct {
	expr     ast.Expr // nil when the variable has no initialiser
	register bool     // the initialiser calls core/data/codec.Register
}

// pkgIndex is the package-level declarations of one package, by name.
type pkgIndex struct {
	vars    map[string]varInit                  // package variables
	order   []string                            // package variables in declaration order
	funcs   map[string]*ast.FuncDecl            // functions without receiver
	methods map[string]map[string]*ast.FuncDecl // receiver type -> method name -> decl
	byName  map[string][]*ast.FuncDecl          // method name -> every method so named
}

// auditRegistrationOrder returns how many package variables files initialise
// through core/data/codec.Register, and one problem per package variable a
// registered codec's key methods read without the initialiser depending on it.
func auditRegistrationOrder(files []*ast.File) (registrations int, problems []string) {
	idx := indexPackage(files)
	for _, name := range idx.order {
		init := idx.vars[name]
		if !init.register {
			continue
		}
		registrations++
		//: what the initialiser orders before itself (spec: references, through functions).
		deps := idx.closure(init.expr, false)
		//: what Register will read, through the methods of every type the initialiser builds.
		for _, typ := range idx.builtTypes(init.expr) {
			for _, m := range registrationReaders {
				decl := idx.methods[typ][m]
				if decl == nil {
					continue
				}
				reads := idx.closure(decl.Body, true)
				for _, v := range sortedKeys(reads) {
					//: a variable with no initialiser is zero before any initialiser runs.
					if idx.vars[v].expr == nil || deps[v] {
						continue
					}
					problems = append(problems, name+" = Register(...) reads package variable "+strconv.Quote(v)+
						" through "+typ+"."+m+", but its initialiser does not refer to it, so Go's package initialization does not order it first")
				}
			}
		}
	}
	return registrations, problems
}

// indexPackage collects the package-level declarations of files.
func indexPackage(files []*ast.File) *pkgIndex {
	idx := &pkgIndex{
		vars:    map[string]varInit{},
		funcs:   map[string]*ast.FuncDecl{},
		methods: map[string]map[string]*ast.FuncDecl{},
		byName:  map[string][]*ast.FuncDecl{},
	}
	for _, file := range files {
		registerNames := coreCodecNames(file)
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				idx.addFunc(d)
			case *ast.GenDecl:
				if d.Tok != token.VAR {
					continue
				}
				for _, spec := range d.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, n := range vs.Names {
						var expr ast.Expr
						if i < len(vs.Values) {
							expr = vs.Values[i]
						}
						idx.vars[n.Name] = varInit{expr: expr, register: expr != nil && callsRegister(expr, registerNames)}
						idx.order = append(idx.order, n.Name)
					}
				}
			}
		}
	}
	return idx
}

// addFunc files d as a function or as a method of its receiver's type.
func (idx *pkgIndex) addFunc(d *ast.FuncDecl) {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		idx.funcs[d.Name.Name] = d
		return
	}
	typ := d.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	id, ok := typ.(*ast.Ident)
	if !ok {
		return
	}
	if idx.methods[id.Name] == nil {
		idx.methods[id.Name] = map[string]*ast.FuncDecl{}
	}
	idx.methods[id.Name][d.Name.Name] = d
	idx.byName[d.Name.Name] = append(idx.byName[d.Name.Name], d)
}

// coreCodecNames returns the local names file imports core/data/codec under.
func coreCodecNames(file *ast.File) map[string]bool {
	names := map[string]bool{}
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != coreCodecImportPath {
			continue
		}
		local := "codec"
		if imp.Name != nil {
			local = imp.Name.Name
		}
		names[local] = true
	}
	return names
}

// callsRegister reports whether expr calls <core codec>.Register.
func callsRegister(expr ast.Expr, names map[string]bool) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return !found
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Register" {
			if x, ok := sel.X.(*ast.Ident); ok && names[x.Name] {
				found = true
			}
		}
		return !found
	})
	return found
}

// closure returns every package variable node refers to, following the
// functions it references — and, when viaMethods, every method it selects by
// name, since the audit cannot tell a receiver's type from syntax alone and a
// read it over-approximates only makes the guard stricter. Without viaMethods
// it under-approximates the specification's dependencies (it ignores method
// references), which also only makes the guard stricter.
func (idx *pkgIndex) closure(node ast.Node, viaMethods bool) map[string]bool {
	seen := map[*ast.FuncDecl]bool{}
	out := map[string]bool{}
	var walk func(ast.Node)
	walk = func(n ast.Node) {
		ast.Inspect(n, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				//: the selected name is a field or a method, never a package variable.
				walk(x.X)
				if viaMethods {
					for _, m := range idx.byName[x.Sel.Name] {
						if !seen[m] {
							seen[m] = true
							walk(m.Body)
						}
					}
				}
				return false
			case *ast.KeyValueExpr:
				//: a struct literal's key is a field name.
				if _, isIdent := x.Key.(*ast.Ident); !isIdent {
					walk(x.Key)
				}
				walk(x.Value)
				return false
			case *ast.Ident:
				if _, ok := idx.vars[x.Name]; ok {
					out[x.Name] = true
				}
				if f := idx.funcs[x.Name]; f != nil && !seen[f] {
					seen[f] = true
					walk(f.Body)
				}
			}
			return true
		})
	}
	if node != nil {
		walk(node)
	}
	return out
}

// builtTypes returns the package types expr builds with a composite literal,
// directly or in a function it calls: the codecs it may hand to Register.
func (idx *pkgIndex) builtTypes(expr ast.Expr) []string {
	types := map[string]bool{}
	seen := map[*ast.FuncDecl]bool{}
	var walk func(ast.Node)
	walk = func(n ast.Node) {
		ast.Inspect(n, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CompositeLit:
				if id, ok := x.Type.(*ast.Ident); ok && idx.methods[id.Name] != nil {
					types[id.Name] = true
				}
			case *ast.Ident:
				if f := idx.funcs[x.Name]; f != nil && !seen[f] {
					seen[f] = true
					walk(f.Body)
				}
			}
			return true
		})
	}
	walk(expr)
	return sortedKeys(types)
}

// sortedKeys returns m's keys in order, for stable messages.
func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return slices.Clip(keys)
}

// sdkRoot resolves the repository root. Under Bazel the sandbox strips the
// source-tree relationship, so the runfiles root is used when the test runner
// provides it — the mechanism //internal/kernel/errs uses for its AST audits,
// and the reason this package's go_test carries data = ["//:audit_sources"].
func sdkRoot(tb testing.TB) string {
	tb.Helper()
	if srcdir, workspace := os.Getenv("TEST_SRCDIR"), os.Getenv("TEST_WORKSPACE"); srcdir != "" && workspace != "" {
		bazelRoot := filepath.Join(srcdir, workspace)
		if _, err := os.Stat(filepath.Join(bazelRoot, "go.work")); err == nil {
			return bazelRoot
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		tb.Fatalf("cwd lookup failed: %v", err)
	}
	for dir := cwd; dir != "/" && dir != ""; dir = filepath.Dir(dir) {
		if _, statErr := os.Stat(filepath.Join(dir, "go.work")); statErr == nil {
			return dir
		}
	}
	tb.Fatalf("go.work not found above %s; this audit needs the workspace layout", cwd)
	return ""
}
