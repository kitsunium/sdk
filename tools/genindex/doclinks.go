// Package main — the doc-link check: every same-package doc link a comment
// writes must name something the package that writes it declares (ADR 0138).
package main

import (
	"fmt"
	"go/ast"
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
		fmt.Fprintf(out, "%s: %s names no symbol this package declares\n", d.pos(), d.text)
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
	byPackage := map[string][]*ast.File{}
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
		byPackage[f.Name.Name] = append(byPackage[f.Name.Name], f)
	}
	var out []deadLink
	//: one production package per directory, plus a stray generator file's.
	for _, files := range byPackage {
		found, derr := deadLinksInPackage(fset, files, dir, root)
		//: go/doc only fails on a malformed file set, which the parser accepted.
		if derr != nil {
			//: surface it to the walk.
			return nil, derr
		}
		out = append(out, found...)
	}
	//: whatever the directory's packages wrote.
	return out, nil
}

// isSourceFile reports whether a file name is a production Go file.
func isSourceFile(name string) bool {
	//: a _test.go file documents nothing go/doc renders.
	return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
}

// deadLinksInPackage checks every doc comment of one package against the
// symbols go/doc resolves for it.
//
// The comments are collected BEFORE go/doc sees the files. Building the package
// without doc.AllDecls removes every unexported top-level declaration from the
// AST it is given — PreserveAST does not stop that — and with the declaration
// goes its doc comment, which `go doc -u` and gopls still render. Collected
// after, those comments were never checked; measured on this repository, that
// hid dead links in unexported declarations entirely.
func deadLinksInPackage(fset *token.FileSet, files []*ast.File, dir, root string) (dead []deadLink, err error) {
	var groups []*ast.CommentGroup
	//: every doc comment, while the files still hold every declaration.
	for _, f := range files {
		groups = append(groups, docComments(f)...)
	}
	//: mode 0: the symbol table is what go/doc renders for the package.
	pkg, derr := doc.NewFromFiles(fset, files, filepath.ToSlash(dir))
	//: without the package's symbol table nothing can be judged.
	if derr != nil {
		//: surface it to the caller.
		return nil, derr
	}
	lookup := pkg.Parser().LookupSym
	var out []deadLink
	//: each comment go/doc, gopls or pkg.go.dev would render.
	for _, group := range groups {
		out = append(out, deadInGroup(fset, group, lookup, root)...)
	}
	//: every dead link the package writes.
	return out, nil
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

// docComments returns every comment group Go treats as documentation: the
// package clause's, each declaration's and each spec's, and each struct field's
// and interface method's, which gopls renders on hover.
func docComments(f *ast.File) []*ast.CommentGroup {
	var out []*ast.CommentGroup
	add := func(g *ast.CommentGroup) {
		//: a declaration without a comment contributes nothing.
		if g != nil {
			out = append(out, g)
		}
	}
	add(f.Doc)
	ast.Inspect(f, func(n ast.Node) bool {
		//: the node kinds that carry a doc comment.
		switch v := n.(type) {
		//: a grouped or single declaration.
		case *ast.GenDecl:
			add(v.Doc)
		//: a function or method.
		case *ast.FuncDecl:
			add(v.Doc)
		//: one type in a group.
		case *ast.TypeSpec:
			add(v.Doc)
		//: one const or var in a group.
		case *ast.ValueSpec:
			add(v.Doc)
		//: a struct field or an interface method.
		case *ast.Field:
			add(v.Doc)
		//: every other node carries no doc comment of its own.
		default:
		}
		//: keep descending: fields sit deep inside type specs.
		return true
	})
	//: every doc comment of the file.
	return out
}
