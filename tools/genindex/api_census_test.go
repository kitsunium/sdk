// Package main — docs/api against a census of the repository with go/parser
// and go/build alone, which shares no code with the writer: it reads what the
// writer read another way. And docs/api's codes against docs/error-codes.yaml,
// which -write-error-codes writes from them: errcodes_test.go holds that file
// to the sources read with go/parser, independently.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	// repoRoot is the repository's root, relative to this package.
	repoRoot string = "../.."
	// errsMasks counts the errs package's masks, MaskByMajor to MaskExact:
	// constants of the code type that are no code.
	errsMasks int = 4
)

// needRepository skips a test that reads the whole repository where it is
// not: a Bazel sandbox stages this package's data alone. `go test` runs it —
// CI's test-386 job and any local run (rule 12).
func needRepository(t *testing.T) {
	t.Helper()
	for _, p := range []string{"go.work", "docs/api/sdk.json", "docs/error-codes.yaml"} {
		if _, err := os.Stat(filepath.Join(repoRoot, p)); err != nil {
			t.Skipf("needs the repository checkout (%s): `go test` in tools/genindex runs this test", p)
		}
	}
}

// census counts one module's exported symbols, each once per package by name:
// a symbol declared in several files, one per platform, counts once.
type census struct {
	// funcs are package-level functions, generic ones included.
	funcs map[string]bool
	// methods are methods declared on an exported type.
	methods map[string]bool
	// ifaceMethods are exported explicit methods of exported interfaces.
	ifaceMethods map[string]bool
	// types are defined types; aliases are counted apart.
	types map[string]bool
	// aliases are type aliases.
	aliases map[string]bool
	// consts are constants.
	consts map[string]bool
	// vars are package-level variables.
	vars map[string]bool
	// aliasMembers counts, per alias, the methods its owner declares: the
	// members pkg/v1's aliases reach, counted apart.
	aliasMembers int
}

// newCensus returns an empty census.
func newCensus() *census {
	return &census{
		funcs: map[string]bool{}, methods: map[string]bool{}, ifaceMethods: map[string]bool{},
		types: map[string]bool{}, aliases: map[string]bool{}, consts: map[string]bool{}, vars: map[string]bool{},
	}
}

// counts renders a census as one comparable line.
func (c *census) counts() string {
	return fmt.Sprintf("funcs %d, methods %d, interface methods %d, types %d, aliases %d, consts %d, vars %d, alias members %d",
		len(c.funcs), len(c.methods), len(c.ifaceMethods), len(c.types), len(c.aliases), len(c.consts), len(c.vars), c.aliasMembers)
}

// parsedPkg is one package of the census: its import path, its files, and
// the names of its aliases and their targets.
type parsedPkg struct {
	// path is the import path.
	path string
	// files are the files some cell compiles.
	files []*ast.File
}

// workModules reads go.work's use list and each module's path.
func workModules(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, "go.work"))
	if err != nil {
		t.Fatalf("reading go.work: %v", err)
	}
	out := map[string]string{}
	inUse := false
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "use ("):
			inUse = true
		case inUse && line == ")":
			inUse = false
		case inUse && line != "":
			dir := path.Clean(line)
			out[dir] = modulePath(t, filepath.Join(repoRoot, filepath.FromSlash(dir), "go.mod"))
		}
	}
	return out
}

// modulePath reads a go.mod's module line.
func modulePath(t *testing.T, gomod string) string {
	t.Helper()
	f, err := os.Open(gomod)
	if err != nil {
		t.Fatalf("reading %s: %v", gomod, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("%s has no module line", gomod)
	return ""
}

// parseModule parses every package of a module that some cell compiles: test
// files, testdata, vendor, dot and underscore directories, any directory
// holding a go.mod of its own and main packages left out, as the go command
// leaves them.
func parseModule(t *testing.T, dir, modPath string, cells []platform) []parsedPkg {
	t.Helper()
	root := filepath.Join(repoRoot, filepath.FromSlash(dir))
	fset := token.NewFileSet()
	var out []parsedPkg
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		base := d.Name()
		if p != root && (base == "testdata" || base == "vendor" || strings.HasPrefix(base, ".") || strings.HasPrefix(base, "_") || hasGoMod(p)) {
			return filepath.SkipDir
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		importPath := modPath
		if rel != "." {
			importPath += "/" + filepath.ToSlash(rel)
		}
		if pkg, ok := parseDir(t, fset, p, importPath, cells); ok {
			out = append(out, pkg)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return out
}

// hasGoMod reports whether a directory is a module's root.
func hasGoMod(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil
}

// parseDir parses the production files of one directory that some cell
// compiles; ok is false for a directory with none, or a main package.
func parseDir(t *testing.T, fset *token.FileSet, dir, importPath string, cells []platform) (parsedPkg, bool) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	pkg := parsedPkg{path: importPath}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || !anyCell(t, dir, name, cells) {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s/%s: %v", dir, name, err)
		}
		if f.Name.Name == "main" {
			return parsedPkg{}, false
		}
		pkg.files = append(pkg.files, f)
	}
	return pkg, len(pkg.files) > 0
}

// anyCell reports whether some cell's go command compiles the file, cgo off.
func anyCell(t *testing.T, dir, name string, cells []platform) bool {
	t.Helper()
	for _, c := range cells {
		ctx := build.Default
		ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = c.goos, c.goarch, false
		ok, err := ctx.MatchFile(dir, name)
		if err != nil {
			t.Fatalf("matching %s/%s: %v", dir, name, err)
		}
		if ok {
			return true
		}
	}
	return false
}

// count adds one package's declarations to a census.
func (c *census) count(pkg parsedPkg) {
	for _, f := range pkg.files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				c.countFunc(pkg.path, d)
			case *ast.GenDecl:
				c.countGen(pkg.path, d)
			}
		}
	}
}

// countFunc counts an exported function, or an exported method of an
// exported type.
func (c *census) countFunc(pkgPath string, d *ast.FuncDecl) {
	if !d.Name.IsExported() {
		return
	}
	if d.Recv == nil {
		c.funcs[pkgPath+"."+d.Name.Name] = true
		return
	}
	if base, ok := recvBaseName(d.Recv.List[0].Type); ok && ast.IsExported(base) {
		c.methods[pkgPath+"."+base+"."+d.Name.Name] = true
	}
}

// recvBaseName is a receiver's type name: T for T, *T, T[K] and *T[K, V].
func recvBaseName(e ast.Expr) (string, bool) {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.Ident:
			return t.Name, true
		default:
			return "", false
		}
	}
}

// countGen counts the exported names of a const, var or type declaration,
// and an exported interface's exported explicit methods.
func (c *census) countGen(pkgPath string, d *ast.GenDecl) {
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.ValueSpec:
			for _, n := range s.Names {
				if !n.IsExported() {
					continue
				}
				if d.Tok == token.CONST {
					c.consts[pkgPath+"."+n.Name] = true
				} else {
					c.vars[pkgPath+"."+n.Name] = true
				}
			}
		case *ast.TypeSpec:
			c.countType(pkgPath, s)
		}
	}
}

// countType counts an exported type or alias, and an exported interface's
// exported explicit methods.
func (c *census) countType(pkgPath string, s *ast.TypeSpec) {
	if !s.Name.IsExported() {
		return
	}
	id := pkgPath + "." + s.Name.Name
	if s.Assign.IsValid() {
		c.aliases[id] = true
		return
	}
	c.types[id] = true
	it, ok := s.Type.(*ast.InterfaceType)
	if !ok {
		return
	}
	for _, m := range it.Methods.List {
		for _, n := range m.Names {
			if n.IsExported() {
				c.ifaceMethods[id+"."+n.Name] = true
			}
		}
	}
}

// aliasTargets maps each alias of the parsed packages to the type it names:
// its import path and name, through the file's imports, type arguments
// dropped; an alias of an unnamed type, or of a type outside the repository,
// names nothing the census counts.
func aliasTargets(pkgs []parsedPkg, names map[string]string) map[string]string {
	out := map[string]string{}
	for _, pkg := range pkgs {
		for _, f := range pkg.files {
			imports := fileImports(f, names)
			for _, d := range f.Decls {
				g, ok := d.(*ast.GenDecl)
				if !ok || g.Tok != token.TYPE {
					continue
				}
				for _, spec := range g.Specs {
					if s := spec.(*ast.TypeSpec); s.Assign.IsValid() && s.Name.IsExported() {
						if target, ok := typeTarget(s.Type, pkg.path, imports); ok {
							out[pkg.path+"."+s.Name.Name] = target
						}
					}
				}
			}
		}
	}
	return out
}

// fileImports maps the names a file imports packages under to their paths.
func fileImports(f *ast.File, names map[string]string) map[string]string {
	out := map[string]string{}
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := names[p]
		if name == "" {
			name = path.Base(p)
		}
		if spec.Name != nil {
			name = spec.Name.Name
		}
		out[name] = p
	}
	return out
}

// typeTarget names the type an alias's right-hand side is: path.Name.
func typeTarget(e ast.Expr, pkgPath string, imports map[string]string) (string, bool) {
	for {
		switch t := e.(type) {
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.Ident:
			return pkgPath + "." + t.Name, true
		case *ast.SelectorExpr:
			x, ok := t.X.(*ast.Ident)
			if !ok || imports[x.Name] == "" {
				return "", false
			}
			return imports[x.Name] + "." + t.Sel.Name, true
		default:
			return "", false
		}
	}
}

// membersOf counts the methods a defined type declares, concrete or of an
// interface, following aliases to their owner.
func membersOf(target string, targets map[string]string, all *census) int {
	for range len(targets) + 1 {
		next, isAlias := targets[target]
		if !isAlias {
			break
		}
		target = next
	}
	n := 0
	for id := range all.methods {
		if strings.HasPrefix(id, target+".") {
			n++
		}
	}
	for id := range all.ifaceMethods {
		if strings.HasPrefix(id, target+".") {
			n++
		}
	}
	return n
}

// Test_apiCensus holds docs/api to an independent census of the repository:
// per module of go.work, the exported functions, methods, interface methods,
// types, aliases, constants and variables go/parser finds in the files some
// cell compiles — each once per package by name — and the members pkg/v1's
// aliases and every other alias reach at their owner, counted apart, equal
// the distinct ids docs/api records of each kind.
func Test_apiCensus(t *testing.T) {
	t.Parallel()
	needRepository(t)
	cells := sdkCells(t)
	modules := workModules(t)
	parsed := map[string][]parsedPkg{}
	names := map[string]string{}
	all := newCensus()
	for dir, mod := range modules {
		parsed[mod] = parseModule(t, dir, mod, cells)
		for _, pkg := range parsed[mod] {
			names[pkg.path] = pkg.files[0].Name.Name
			all.count(pkg)
		}
	}
	targets := aliasTargets(slices.Concat(slices.Collect(maps.Values(parsed))...), names)
	docs := readAllDocuments(t)
	root := modules["."]
	for _, mod := range slices.Sorted(maps.Values(modules)) {
		c := newCensus()
		for _, pkg := range parsed[mod] {
			c.count(pkg)
		}
		for alias := range c.aliases {
			if target, ok := targets[alias]; ok {
				c.aliasMembers += membersOf(target, targets, all)
			}
		}
		doc, ok := docs[documentName(root, mod)]
		if !ok {
			t.Errorf("%s: no document in docs/api", mod)
			continue
		}
		if got, want := documentCensus(doc, docs).counts(), c.counts(); got != want {
			t.Errorf("%s:\n  docs/api: %s\n  census:   %s", mod, got, want)
			continue
		}
		t.Logf("%s: %s", mod, c.counts())
	}
}

// readAllDocuments reads every document under docs/api, by name.
func readAllDocuments(t *testing.T) map[string]*apiDocument {
	t.Helper()
	names, err := existingDocuments(repoRoot)
	if err != nil {
		t.Fatalf("listing docs/api: %v", err)
	}
	out := map[string]*apiDocument{}
	for _, name := range names {
		raw, err := os.ReadFile(documentPath(repoRoot, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		doc := &apiDocument{}
		if err := json.Unmarshal(raw, doc); err != nil {
			t.Fatalf("decoding %s: %v", name, err)
		}
		out[name] = doc
	}
	return out
}

// documentCensus counts a document's distinct ids by kind, an interface's
// methods apart, and the methods every alias's owner declares, across every
// document.
func documentCensus(doc *apiDocument, docs map[string]*apiDocument) *census {
	c := newCensus()
	interfaces := map[string]bool{}
	byOwner := map[string]int{}
	for _, d := range docs {
		for _, s := range d.Symbols {
			if s.Kind == kindType && strings.HasPrefix(stripTypeParams(s.Canonical), "interface{") {
				interfaces[s.Package+"."+s.Name] = true
			}
		}
	}
	seen := map[string]bool{}
	for _, d := range docs {
		for _, s := range d.Symbols {
			if s.Kind == kindMethod && !seen[s.ID] {
				seen[s.ID] = true
				byOwner["go:"+escapePath(s.Package)+"."+s.Recv]++
			}
		}
	}
	for _, s := range doc.Symbols {
		key := s.Package + "." + s.Name
		switch s.Kind {
		case kindFunc:
			c.funcs[key] = true
		case kindMethod:
			if interfaces[s.Package+"."+s.Recv] {
				c.ifaceMethods[s.ID] = true
			} else {
				c.methods[s.ID] = true
			}
		case kindType:
			c.types[key] = true
		case kindAlias:
			if !c.aliases[key] && s.Owner != "" {
				c.aliasMembers += byOwner[s.Owner]
			}
			c.aliases[key] = true
		case kindConst:
			c.consts[key] = true
		case kindVar:
			c.vars[key] = true
		}
	}
	return c
}

// stripTypeParams drops a leading type parameter list from a canonical
// signature.
func stripTypeParams(sig string) string {
	if params, rest := splitTypeParams(sig); params != "" {
		return strings.TrimSpace(rest)
	}
	return sig
}

// Test_apiCodes holds the codes docs/api records to docs/error-codes.yaml,
// which tools/genindex -write-error-codes writes from docs/api: every errs.Code
// constant a package declares under a name starting with Code — not one it
// re-exports — is the same set of (package, name, dotted quad) in both, the
// errs package's six meta-codes included; its four masks, of the code type and
// no code, are the only declared constants of it the file leaves out, and the
// test names them. Every sentinel docs/api records carries a reason, a public
// text and a code the YAML lists. Test_errorCodesCensus holds the same file
// to the sources, read with go/parser alone.
func Test_apiCodes(t *testing.T) {
	t.Parallel()
	needRepository(t)
	yaml := readErrorCodes(t)
	declared := map[string]bool{}
	listed := map[string]bool{}
	for entry := range yaml {
		listed[entry[strings.LastIndexByte(entry, ' ')+1:]] = true
	}
	sentinels := 0
	var masks []string
	for _, doc := range readAllDocuments(t) {
		for _, s := range doc.Symbols {
			if s.Code == nil {
				continue
			}
			if s.Kind == kindConst && s.Init == "" && !strings.HasPrefix(s.Name, "Code") {
				masks = append(masks, s.ID+" "+s.Code.Value)
				continue
			}
			if s.Kind == kindConst && s.Init == "" {
				declared[strings.TrimPrefix(s.Package, "github.com/kitsunium/sdk/")+"."+s.Name+" "+s.Code.Value] = true
			}
			if s.Kind == kindVar {
				sentinels++
				if s.Code.Reason == "" || s.Code.Public == "" || !listed[s.Code.Value] {
					t.Errorf("sentinel %s: code %+v is incomplete or not in docs/error-codes.yaml", s.ID, *s.Code)
				}
			}
		}
	}
	for entry := range yaml {
		if !declared[entry] {
			t.Errorf("docs/error-codes.yaml lists %s; docs/api declares no such code", entry)
		}
	}
	for entry := range declared {
		if !yaml[entry] {
			t.Errorf("docs/api declares %s; docs/error-codes.yaml does not list it", entry)
		}
	}
	slices.Sort(masks)
	if len(masks) != errsMasks || slices.ContainsFunc(masks, func(m string) bool { return !strings.HasPrefix(m, "go:"+errsPath+".Mask") }) {
		t.Errorf("the constants of the code type that are no code = %q, want the errs package's %d masks", masks, errsMasks)
	}
	t.Logf("%d codes in both; %d sentinel records; the errs package's masks, outside the YAML: %s", len(yaml), sentinels, strings.Join(masks, ", "))
}

// readErrorCodes reads docs/error-codes.yaml as "package.Name dotted-quad".
func readErrorCodes(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, "docs", "error-codes.yaml"))
	if err != nil {
		t.Fatalf("reading docs/error-codes.yaml: %v", err)
	}
	out := map[string]bool{}
	var code, name string
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "- code: "):
			code = strings.Trim(strings.TrimPrefix(line, "- code: "), `"`)
		case strings.HasPrefix(line, "const: "):
			name = strings.TrimPrefix(line, "const: ")
		case strings.HasPrefix(line, "package: "):
			out[strings.TrimPrefix(line, "package: ")+"."+name+" "+code] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("docs/error-codes.yaml lists no code")
	}
	return out
}
