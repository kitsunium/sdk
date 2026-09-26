// Package main — the doc-link check: every same-package doc link a comment
// writes must name something the package that writes it declares (ADR 0138).
package main

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/doc"
	"go/doc/comment"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// docLinkAdvice is printed under the findings. The dominant cause is not a
// typo: go/doc collects methods and fields from the declarations of the package
// it documents, and a pkg/v1 facade ALIASES its types, so a link to a method of
// an aliased Broker resolves nothing there while a link to Broker itself does.
const docLinkAdvice string = "a member of an ALIASED type cannot be a doc link in the package that aliases it — write [Type].Member, which links the alias and reads the same (ADR 0138)"

// platforms are the targets the cross-build lane compiles (ADR 0137). A
// package's symbols differ between them — a _windows.go file declares what a
// _linux.go one does not — and pkg.go.dev renders a package per build context,
// so a comment is judged against what its package declares on each platform
// that compiles the file holding it. One table over the union of every file
// would accept a link to a symbol no single platform has.
var platforms = []platform{
	{goos: "linux", goarch: "amd64"},
	{goos: "linux", goarch: "arm64"},
	{goos: "linux", goarch: "386"},
	{goos: "linux", goarch: "arm"},
	{goos: "darwin", goarch: "arm64"},
	{goos: "windows", goarch: "amd64"},
	{goos: "freebsd", goarch: "amd64"},
	{goos: "openbsd", goarch: "amd64"},
	{goos: "netbsd", goarch: "amd64"},
	{goos: "dragonfly", goarch: "amd64"},
}

// platform is one GOOS/GOARCH pair a doc comment is judged under.
type platform struct {
	// goos is the target operating system.
	goos string
	// goarch is the target architecture.
	goarch string
}

// deadLink is one bracketed name go/doc renders as literal text because the
// package declares no such symbol.
type deadLink struct {
	// file is the file that writes it, relative to the checked root, in slashes.
	file string
	// line is the line that writes it.
	line int
	// col is the column of the comment on that line.
	col int
	// text is the link as written, brackets included.
	text string
	// on names the platforms the link is dead on, when it resolves on others
	// that compile the same file; empty when it is dead wherever the file builds.
	on string
}

// String renders a platform the way GOOS/GOARCH is written.
func (p platform) String() string {
	//: the conventional spelling.
	return p.goos + "/" + p.goarch
}

// matches reports whether the go command compiles the named file of dir for
// this platform: its name suffix and its build constraints, with cgo off, as
// every lane of this repository builds.
func (p platform) matches(dir, name string) (bool, error) {
	ctx := build.Default
	ctx.GOOS = p.goos
	ctx.GOARCH = p.goarch
	ctx.CgoEnabled = false
	//: the go command's own answer, not a re-implementation of it.
	return ctx.MatchFile(dir, name)
}

// pos renders where a link is written in the file:line:col form editors jump to.
func (d deadLink) pos() string {
	//: one form for every report line.
	return fmt.Sprintf("%s:%d:%d", d.file, d.line, d.col)
}

// compareDeadLinks orders findings by file, then line, then column, then text —
// numerically, so line 10 comes after line 9 and not after line 1.
func compareDeadLinks(a, b deadLink) int {
	//: the file first.
	if c := strings.Compare(a.file, b.file); c != 0 {
		//: different files order by path.
		return c
	}
	//: then the line.
	if a.line != b.line {
		//: numerically.
		return a.line - b.line
	}
	//: then the column.
	if a.col != b.col {
		//: numerically.
		return a.col - b.col
	}
	//: same place: order by the link text.
	return strings.Compare(a.text, b.text)
}

// runDocLinkCheck walks every root, prints each dead same-package doc link to
// out, and returns the process exit status: 0 when every link resolves, 1 when
// one does not or a package could not be read.
func runDocLinkCheck(roots []string, out io.Writer) int {
	//: without a root there is nothing to check, and "nothing checked" must not
	//: read as "nothing wrong".
	if len(roots) == 0 {
		fmt.Fprintln(out, "genindex: -check-doclinks needs at least one directory to walk")
		//: a usage error, not a clean tree.
		return 1
	}
	var dead []deadLink
	//: every root contributes its findings; one unreadable root fails the run.
	for _, root := range roots {
		found, err := checkDocLinks(root)
		//: a package that will not parse would hide every link it holds.
		if err != nil {
			fmt.Fprintf(out, "genindex: %v\n", err)
			//: an incomplete check is not a passing one.
			return 1
		}
		dead = append(dead, found...)
	}
	//: every doc link resolves: nothing to report.
	if len(dead) == 0 {
		fmt.Fprintf(out, "genindex: every same-package doc link resolves under %s\n", strings.Join(roots, " "))
		//: the check passed.
		return 0
	}
	//: one line per finding, in the file:line:col form editors jump to.
	for _, d := range dead {
		where := ""
		//: a link dead on some platforms only says which.
		if d.on != "" {
			where = " on " + d.on
		}
		fmt.Fprintf(out, "%s: %s names no symbol this package declares%s\n", d.pos(), d.text, where)
	}
	fmt.Fprintf(out, "genindex: %d dead doc link(s); %s\n", len(dead), docLinkAdvice)
	//: at least one link renders as literal text.
	return 1
}

// checkDocLinks returns every dead same-package doc link under root, sorted by
// position.
//
// Same-package only: a link qualified by a package name resolves through the
// file's imports to a package go/doc cannot see from here, so it can only be
// judged by the package it names; and a lowercase name in brackets is never a
// link at all.
func checkDocLinks(root string) (dead []deadLink, err error) {
	abs, aerr := filepath.Abs(root)
	//: positions are reported relative to this, so it has to resolve.
	if aerr != nil {
		//: report the root that could not be resolved.
		return nil, fmt.Errorf("resolve %s: %w", root, aerr)
	}
	var out []deadLink
	walkErr := filepath.WalkDir(abs, func(path string, d os.DirEntry, walkErr error) error {
		//: a directory the walk could not read aborts the check.
		if walkErr != nil {
			//: surface it to WalkDir, which stops the walk.
			return walkErr
		}
		//: only directories carry packages; their files are read as a set.
		if !d.IsDir() {
			//: nothing to do for a file.
			return nil
		}
		//: the same pruning as the index: dot dirs, vendor, testdata, node_modules.
		if path != abs && skipDir(filepath.Base(path)) {
			//: prune the whole subtree.
			return filepath.SkipDir
		}
		found, dirErr := deadLinksInDir(path, abs)
		//: a directory that will not parse would hide its links.
		if dirErr != nil {
			//: name the directory that failed.
			return fmt.Errorf("check %s: %w", path, dirErr)
		}
		out = append(out, found...)
		//: this directory is done.
		return nil
	})
	//: the first failure aborts the whole check.
	if walkErr != nil {
		//: surface it unchanged; it already names the directory.
		return nil, walkErr
	}
	slices.SortFunc(out, compareDeadLinks)
	//: every dead link under the root.
	return out, nil
}

// deadLinksInDir parses one directory's production files and checks each
// package it holds. Test files are left out: go/doc does not render them.
func deadLinksInDir(dir, root string) (dead []deadLink, err error) {
	entries, rerr := os.ReadDir(dir)
	//: an unreadable directory would hide its links.
	if rerr != nil {
		//: surface it to the walk.
		return nil, rerr
	}
	fset := token.NewFileSet()
	byPackage := map[string][]parsedFile{}
	//: every production Go file, grouped by the package clause it declares.
	for _, e := range entries {
		//: only production Go files are documentation go/doc renders.
		if e.IsDir() || !isSourceFile(e.Name()) {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ParseComments)
		//: a file that will not parse cannot be checked honestly.
		if perr != nil {
			//: surface the parse failure.
			return nil, perr
		}
		byPackage[f.Name.Name] = append(byPackage[f.Name.Name], parsedFile{name: e.Name(), ast: f})
	}
	var out []deadLink
	//: one production package per directory, plus a stray generator file's.
	for _, files := range byPackage {
		found, derr := deadLinksInPackage(fset, files, dir, root)
		//: a file the go command could not classify, or a malformed file set.
		if derr != nil {
			//: surface it to the walk.
			return nil, derr
		}
		out = append(out, found...)
	}
	//: whatever the directory's packages wrote.
	return out, nil
}

// parsedFile is one production file of a package: its base name, which build
// constraints are matched against, and its syntax tree.
type parsedFile struct {
	// name is the file's base name.
	name string
	// ast is the parsed file, comments included.
	ast *ast.File
}

// isSourceFile reports whether a file name is a production Go file.
func isSourceFile(name string) bool {
	//: a _test.go file documents nothing go/doc renders.
	return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
}

// deadLinksInPackage checks every doc comment of one package, on every platform
// that compiles the file holding it, against the symbols go/doc resolves for
// the package on that platform.
//
// The comments are collected once, before go/doc sees the files, and the
// package is built with doc.AllDecls|doc.PreserveAST: without AllDecls, go/doc
// removes every unexported declaration from the AST it is given — PreserveAST
// does not stop that — and with the declaration goes its doc comment, which
// `go doc -u` and gopls still render. A first version of this check lost eleven
// dead links that way. AllDecls resolves exactly the same same-package links: a
// link's name must be upper-case, so the unexported symbols it adds are ones no
// link can name.
func deadLinksInPackage(fset *token.FileSet, files []parsedFile, dir, root string) (dead []deadLink, err error) {
	check := newPackageCheck(fset, files, dir, root)
	//: one symbol table per platform, over the files that platform compiles.
	for _, p := range platforms {
		//: a file the go command cannot classify cannot be judged honestly.
		if jerr := check.judgeOn(p); jerr != nil {
			//: surface it to the caller.
			return nil, jerr
		}
	}
	//: every dead link the package writes, with the platforms it is dead on.
	return check.findings(), nil
}

// packageCheck is one package's doc-link check across the platforms.
type packageCheck struct {
	// fset positions every file of the package.
	fset *token.FileSet
	// files are the package's production files.
	files []parsedFile
	// comments holds each file's doc comments, collected before go/doc runs.
	comments map[*ast.File][]*ast.CommentGroup
	// dir is the package directory.
	dir string
	// root is the checked root positions are relative to.
	root string
	// deadOn lists, per finding, the platforms it is dead on.
	deadOn map[deadLink][]string
	// builtOn counts, per finding, the platforms that compile its file.
	builtOn map[deadLink]int
	// order is the order findings were first seen in.
	order []deadLink
}

// newPackageCheck collects every doc comment of the package while its files
// still hold every declaration.
func newPackageCheck(fset *token.FileSet, files []parsedFile, dir, root string) *packageCheck {
	comments := make(map[*ast.File][]*ast.CommentGroup, len(files))
	//: each file's doc comments, once, whatever platform later reads them.
	for _, f := range files {
		comments[f.ast] = docComments(f.ast)
	}
	//: a check with nothing recorded yet.
	return &packageCheck{
		fset: fset, files: files, comments: comments, dir: dir, root: root,
		deadOn: map[deadLink][]string{}, builtOn: map[deadLink]int{},
	}
}

// judgeOn checks each comment of each file p compiles against the package's
// symbols on p.
func (c *packageCheck) judgeOn(p platform) error {
	built, ferr := filesFor(p, c.files, c.dir)
	//: a file whose header cannot be read cannot be placed on a platform.
	if ferr != nil {
		//: surface it to the caller.
		return ferr
	}
	//: nothing of this package builds here.
	if len(built) == 0 {
		//: no symbol table to judge against.
		return nil
	}
	lookup, serr := symbolsOf(c.fset, built, c.dir)
	//: without the package's symbol table nothing can be judged.
	if serr != nil {
		//: surface it to the caller.
		return serr
	}
	//: each compiled file, each of its doc comments, each dead link in it.
	for _, f := range built {
		//: every doc comment the file holds.
		for _, group := range c.comments[f.ast] {
			//: every dead link the comment writes, where it writes it.
			for _, d := range deadInGroup(c.fset, group, lookup, c.root) {
				c.record(d, p, f)
			}
		}
	}
	//: this platform is judged.
	return nil
}

// record notes that d is dead on p, the first time with how many platforms
// compile the file that writes it.
func (c *packageCheck) record(d deadLink, p platform, file parsedFile) {
	//: first sighting: keep the order and count the file's platforms once.
	if _, seen := c.deadOn[d]; !seen {
		c.order = append(c.order, d)
		c.builtOn[d] = platformsBuilding(file, c.dir)
	}
	c.deadOn[d] = append(c.deadOn[d], p.String())
}

// findings returns one finding per dead link, naming its platforms only when
// it resolves on some of the platforms that compile it.
func (c *packageCheck) findings() []deadLink {
	out := make([]deadLink, 0, len(c.order))
	//: in the order they were first seen.
	for _, d := range c.order {
		//: dead on only part of where the file builds.
		if len(c.deadOn[d]) < c.builtOn[d] {
			d.on = strings.Join(c.deadOn[d], ", ")
		}
		out = append(out, d)
	}
	//: the package's findings.
	return out
}

// symbolsOf builds the package from the given files and returns go/doc's own
// lookup of a same-package link.
func symbolsOf(fset *token.FileSet, files []parsedFile, dir string) (lookup func(recv, name string) bool, err error) {
	asts := make([]*ast.File, 0, len(files))
	//: the files go/doc sees are the ones this platform compiles.
	for _, f := range files {
		asts = append(asts, f.ast)
	}
	pkg, derr := doc.NewFromFiles(fset, asts, filepath.ToSlash(dir), doc.AllDecls|doc.PreserveAST)
	//: go/doc only fails on a malformed file set, which the parser accepted.
	if derr != nil {
		//: surface it to the caller.
		return nil, derr
	}
	//: the resolution go doc, gopls and pkg.go.dev apply.
	return pkg.Parser().LookupSym, nil
}

// filesFor returns the files of a package the go command compiles for p.
func filesFor(p platform, files []parsedFile, dir string) (built []parsedFile, err error) {
	var out []parsedFile
	//: each file answers for itself: name suffix and build constraints.
	for _, f := range files {
		ok, merr := p.matches(dir, f.name)
		//: an unreadable header is a file we cannot place.
		if merr != nil {
			//: surface it to the caller.
			return nil, merr
		}
		//: compiled on this platform.
		if ok {
			out = append(out, f)
		}
	}
	//: the files this platform builds.
	return out, nil
}

// platformsBuilding counts the platforms that compile a file. An error here was
// already reported by filesFor for the same file and platform, so it counts as
// not building rather than failing twice.
func platformsBuilding(file parsedFile, dir string) int {
	n := 0
	//: every platform the check judges.
	for _, p := range platforms {
		//: the same answer filesFor gave.
		if ok, err := p.matches(dir, file.name); err == nil && ok {
			n++
		}
	}
	//: how many platforms render this file.
	return n
}

// deadInGroup reports the dead same-package links of one doc comment, at the
// line each is written on.
//
// Each distinct link is located once: linkPositions already reports every line
// that writes it, so a link written twice must not be searched for twice.
func deadInGroup(fset *token.FileSet, group *ast.CommentGroup, lookup func(recv, name string) bool, root string) []deadLink {
	var out []deadLink
	seen := map[string]struct{}{}
	//: every same-package link the comment would render if its symbol existed.
	for _, link := range sameScopeLinks(group.Text()) {
		//: a link go/doc resolves renders as a link.
		if lookup(link.Recv, link.Name) {
			continue
		}
		text := linkText(link)
		//: already located, with every line that writes it.
		if _, dup := seen[text]; dup {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, linkPositions(fset, group, text, root)...)
	}
	//: this comment's dead links, where they are written.
	return out
}

// sameScopeLinks parses a doc comment as if every same-package name existed,
// and returns the links that parse yields with no import path: exactly the
// forms that would be links if the package declared them.
//
// LookupPackage always refuses, so a link qualified by a package name is not a
// candidate — except a standard-library one, which go/doc resolves on its own
// and which therefore carries an import path and is filtered out below. An
// empty name is what a slice type's brackets parse to under a permissive
// lookup; it names nothing.
func sameScopeLinks(text string) []*comment.DocLink {
	permissive := &comment.Parser{
		LookupPackage: func(string) (string, bool) {
			//: no qualified name is judged here.
			return "", false
		},
		LookupSym: func(_, _ string) bool {
			//: every same-package name is a candidate.
			return true
		},
	}
	var out []*comment.DocLink
	//: every block that can carry inline text.
	for _, block := range permissive.Parse(text).Content {
		out = collectDocLinks(block, out)
	}
	//: the candidates, in the order they are written.
	return out
}

// collectDocLinks appends the same-package doc links of one block.
//
// A heading is not searched: go/doc/comment never parses a link in one (its
// text stays plain, measured), so a bracketed name there is literal by design.
func collectDocLinks(block comment.Block, out []*comment.DocLink) []*comment.DocLink {
	//: only paragraphs and list items carry inline links.
	switch b := block.(type) {
	//: prose.
	case *comment.Paragraph:
		//: its inline text.
		return appendDocLinks(b.Text, out)
	//: a list holds blocks of its own.
	case *comment.List:
		//: every item's blocks.
		for _, item := range b.Items {
			//: each block of the item.
			for _, inner := range item.Content {
				out = collectDocLinks(inner, out)
			}
		}
		//: the list's links.
		return out
	//: a code block renders verbatim and a heading stays plain: no link.
	default:
		//: nothing to add.
		return out
	}
}

// appendDocLinks appends the same-package doc links found in inline text.
func appendDocLinks(text []comment.Text, out []*comment.DocLink) []*comment.DocLink {
	//: every inline element.
	for _, t := range text {
		//: only a doc link, or a URL link's own text, can hold one.
		switch v := t.(type) {
		//: a same-package link has no import path and names something.
		case *comment.DocLink:
			//: a qualified or nameless link is not ours to judge.
			if v.ImportPath == "" && v.Name != "" {
				out = append(out, v)
			}
		//: the text of a [text]: URL link.
		case *comment.Link:
			out = appendDocLinks(v.Text, out)
		//: plain and italic text hold no link.
		default:
		}
	}
	//: the links found.
	return out
}

// linkText renders a doc link as it is written, without the brackets.
func linkText(link *comment.DocLink) string {
	//: a method or field is written Recv.Name.
	if link.Recv != "" {
		//: the qualified member form.
		return link.Recv + "." + link.Name
	}
	//: a bare top-level name.
	return link.Name
}

// linkPositions finds every line of a comment that writes [text] or [*text]
// and returns one finding per occurrence, positioned at that line.
func linkPositions(fset *token.FileSet, group *ast.CommentGroup, text, root string) []deadLink {
	var out []deadLink
	//: the forms go/doc accepts for the same link.
	forms := []string{"[" + text + "]", "[*" + text + "]"}
	//: every comment line of the group.
	for _, c := range group.List {
		//: each spelling of the link.
		for _, form := range forms {
			//: every occurrence on the line is its own finding.
			for range strings.Count(c.Text, form) {
				out = append(out, newDeadLink(fset.Position(c.Pos()), root, form))
			}
		}
	}
	//: a link the parser saw must be written somewhere; if a reflowed comment
	//: hides it from the line search, report it at the comment itself.
	if len(out) == 0 {
		out = append(out, newDeadLink(fset.Position(group.Pos()), root, "["+text+"]"))
	}
	//: this link's occurrences.
	return out
}

// newDeadLink records a finding at a position, its file made relative to the
// checked root.
func newDeadLink(p token.Position, root, text string) deadLink {
	//: the position's line and column, the file relative to the root.
	return deadLink{file: relFile(p.Filename, root), line: p.Line, col: p.Column, text: text}
}

// relFile renders a file name relative to the checked root, in slash form, or
// unchanged when it is not under it.
func relFile(name, root string) string {
	rel, err := filepath.Rel(root, name)
	//: a name the root cannot relate is reported as it is.
	if err != nil {
		//: unchanged.
		return name
	}
	//: slashes, whatever the host uses, so reports compare across machines.
	return filepath.ToSlash(rel)
}

// docComments returns every comment group documentation renders: the package
// clause's, each top-level declaration's and spec's, and the fields and
// interface methods of each top-level type, which gopls shows on hover.
//
// Only TOP-LEVEL declarations. A comment on a function parameter, on a result,
// or on a field of a struct declared inside a function body is not
// documentation go/doc, gopls or pkg.go.dev publishes, so a bracketed name
// there is not a link anybody sees.
func docComments(f *ast.File) []*ast.CommentGroup {
	var out []*ast.CommentGroup
	out = appendDoc(out, f.Doc)
	//: each top-level declaration, and nothing inside a function body.
	for _, decl := range f.Decls {
		//: the two kinds of top-level declaration.
		switch d := decl.(type) {
		//: a function or method: its own comment, not its parameters'.
		case *ast.FuncDecl:
			out = appendDoc(out, d.Doc)
		//: const, var, type and import declarations.
		case *ast.GenDecl:
			out = appendDoc(out, d.Doc)
			//: each spec of the group.
			for _, spec := range d.Specs {
				out = appendSpecDocs(out, spec)
			}
		//: a bad declaration carries nothing.
		default:
		}
	}
	//: every doc comment of the file.
	return out
}

// appendSpecDocs appends a spec's own comment and, for a type, the comments of
// its fields and interface methods.
func appendSpecDocs(out []*ast.CommentGroup, spec ast.Spec) []*ast.CommentGroup {
	//: only type and value specs carry documentation.
	switch s := spec.(type) {
	//: a type: its comment, then its members.
	case *ast.TypeSpec:
		out = appendDoc(out, s.Doc)
		//: the members documentation shows as part of the type.
		return appendMemberDocs(out, s.Type)
	//: a const or var.
	case *ast.ValueSpec:
		//: its comment.
		return appendDoc(out, s.Doc)
	//: an import spec documents nothing.
	default:
		//: nothing to add.
		return out
	}
}

// appendMemberDocs appends the comments of a type's struct fields and interface
// methods, descending into nested struct and interface literals — shown as part
// of the type — and never into a function type, whose parameter comments no
// documentation renders.
func appendMemberDocs(out []*ast.CommentGroup, expr ast.Expr) []*ast.CommentGroup {
	//: the type expressions that hold documented members themselves.
	switch t := expr.(type) {
	//: a struct: each field, and whatever its type nests.
	case *ast.StructType:
		//: every field of the struct.
		for _, field := range t.Fields.List {
			out = appendDoc(out, field.Doc)
			out = appendMemberDocs(out, field.Type)
		}
		//: the struct's members.
		return out
	//: an interface: each method or embedded element.
	case *ast.InterfaceType:
		//: every element of the method set.
		for _, method := range t.Methods.List {
			out = appendDoc(out, method.Doc)
		}
		//: the interface's members.
		return out
	//: anything else: only what it nests can hold members.
	default:
		//: a pointer, slice, array, map or channel of a literal type.
		for _, inner := range nestedTypes(expr) {
			out = appendMemberDocs(out, inner)
		}
		//: whatever the nested types held.
		return out
	}
}

// nestedTypes returns the type expressions a pointer, array, slice, map or
// channel type is built from; a named type or a function type nests none that
// documentation shows.
func nestedTypes(expr ast.Expr) []ast.Expr {
	//: the composite type expressions.
	switch t := expr.(type) {
	//: a pointer.
	case *ast.StarExpr:
		//: the pointed-to type.
		return []ast.Expr{t.X}
	//: an array or a slice.
	case *ast.ArrayType:
		//: the element type.
		return []ast.Expr{t.Elt}
	//: a map.
	case *ast.MapType:
		//: key and value.
		return []ast.Expr{t.Key, t.Value}
	//: a channel.
	case *ast.ChanType:
		//: the element type.
		return []ast.Expr{t.Value}
	//: a named type, a function type or anything else.
	default:
		//: nothing nested.
		return nil
	}
}

// appendDoc appends a comment group when there is one.
func appendDoc(out []*ast.CommentGroup, g *ast.CommentGroup) []*ast.CommentGroup {
	//: a declaration without a comment contributes nothing.
	if g == nil {
		//: unchanged.
		return out
	}
	//: the comment, in declaration order.
	return append(out, g)
}
