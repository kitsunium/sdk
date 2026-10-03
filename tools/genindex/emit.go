// Package main — projecting a documented package into search rows.
package main

import (
	"go/doc"
	"go/token"
	"strings"
)

// rowContext is everything a row builder needs beyond the declaration itself.
//
// It travels as one value because every field is computed once per package and
// then repeated on every row: passing them separately is how one row ends up
// with another package's label.
type rowContext struct {
	// pkg is the documented package the rows come from.
	pkg *doc.Package
	// fset resolves declaration positions into files and lines.
	fset *token.FileSet
	// pkgLabel prefixes every Qualified name.
	pkgLabel string
	// packageShort is the module-relative package path.
	packageShort string
	// pkgURL is the package's anchor base on the docs site.
	pkgURL string
	// opts carries the extraction parameters.
	opts *indexOptions
}

// emit projects every exported declaration of a package into index rows.
func emit(p *doc.Package, fset *token.FileSet, packageShort string, opts *indexOptions) []symbol {
	ctx := rowContext{
		pkg:          p,
		fset:         fset,
		pkgLabel:     packageLabel(p, packageShort),
		packageShort: packageShort,
		pkgURL:       packageURL(opts.urlBase, packageShort),
		opts:         opts,
	}

	var out []symbol
	//: functions first, then types with their constructors and methods, then
	//: package values — the order a reader scans a package doc page in.
	for _, f := range p.Funcs {
		out = append(out, ctx.funcRow(f))
	}
	//: each type carries its constructors, its methods and its attached
	//: constants and variables.
	for _, t := range p.Types {
		out = append(out, ctx.typeRows(t)...)
	}
	out = append(out, ctx.valueRows(p.Consts, "const")...)
	out = append(out, ctx.valueRows(p.Vars, "var")...)
	//: every exported declaration the package holds.
	return out
}

// packageLabel is the prefix every Qualified name in a package carries.
func packageLabel(p *doc.Package, packageShort string) string {
	//: the module root has no path segment to take.
	if label, ok := lastPathSegment(packageShort); ok {
		//: the final path segment, e.g. "codec".
		return label
	}
	//: fall back to the package's own name so a root symbol reads "Name"
	//: rather than a leading-dot ".Name".
	return p.Name
}

// packageURL is the anchor base every symbol in a package hangs off.
func packageURL(urlBase, packageShort string) string {
	out := strings.TrimSuffix(urlBase, "/")
	//: the module root's page sits at the base itself.
	if packageShort != "." && packageShort != "" {
		out += "/" + packageShort
	}
	//: the docs site serves package pages as directories.
	return out + "/"
}

// funcRow builds the row for a package-level function, whether go/doc lists it
// with the package or files it under the type it returns.
func (c *rowContext) funcRow(f *doc.Func) symbol {
	//: a function is addressed by its bare name within the package — one filed
	//: under a type too, which the docs page anchors by name beneath that type.
	return symbol{
		Kind:         "func",
		Name:         f.Name,
		Qualified:    c.pkgLabel + "." + f.Name,
		Package:      c.pkg.ImportPath,
		PackageShort: c.packageShort,
		Signature:    signatureOf(c.fset, f.Decl),
		Doc:          synopsis(f.Doc),
		URL:          c.pkgURL + "#" + f.Name,
		Examples:     exampleNames(f.Examples),
		Obsolete:     isDeprecated(f.Doc),
		SourceURL:    sourceLinkOf(c.opts, c.fset, f.Decl),
	}
}

// typeRow builds the row for an exported type.
func (c *rowContext) typeRow(t *doc.Type) symbol {
	//: a type is addressed by its bare name within the package.
	return symbol{
		Kind:         "type",
		Name:         t.Name,
		Qualified:    c.pkgLabel + "." + t.Name,
		Package:      c.pkg.ImportPath,
		PackageShort: c.packageShort,
		Signature:    signatureOf(c.fset, t.Decl),
		Doc:          synopsis(t.Doc),
		URL:          c.pkgURL + "#" + t.Name,
		Examples:     exampleNames(t.Examples),
		Obsolete:     isDeprecated(t.Doc),
		SourceURL:    sourceLinkOf(c.opts, c.fset, t.Decl),
	}
}

// typeRows builds the rows of one type: the type itself, the functions go/doc
// files under it, its methods, then its constants and variables.
//
// go/doc files a function under a type when that type is the only result type
// of the package the function names — T, *T or []T, beside any number of
// predeclared or imported ones such as an error or a bool — so a constructor, a
// parser and an accessor returning a facade alias all land in t.Funcs rather
// than in the package's Funcs. They are still package-level functions, so each
// gets the row a package-level function gets. Reading p.Funcs alone kept 303 of
// pkg/v1's 444 functions out of the index.
func (c *rowContext) typeRows(t *doc.Type) []symbol {
	out := []symbol{c.typeRow(t)}
	//: the functions filed under the type, by their own name.
	for _, f := range t.Funcs {
		out = append(out, c.funcRow(f))
	}
	//: methods are indexed under their own name so a search for the method
	//: finds it without knowing the receiver.
	for _, m := range t.Methods {
		out = append(out, c.methodRow(t, m))
	}
	out = append(out, c.valueRows(t.Consts, "const")...)
	out = append(out, c.valueRows(t.Vars, "var")...)
	//: the type and everything go/doc attached to it.
	return out
}

// methodRow builds the row for a method attached to a type.
func (c *rowContext) methodRow(t *doc.Type, m *doc.Func) symbol {
	//: the anchor is Type.Method, which is how the docs site names it, while
	//: Name stays bare so a search for the method alone still finds it.
	return symbol{
		Kind:         "method",
		Name:         m.Name,
		Qualified:    c.pkgLabel + "." + t.Name + "." + m.Name,
		Package:      c.pkg.ImportPath,
		PackageShort: c.packageShort,
		Signature:    signatureOf(c.fset, m.Decl),
		Doc:          synopsis(m.Doc),
		URL:          c.pkgURL + "#" + t.Name + "." + m.Name,
		Receiver:     t.Name,
		Examples:     exampleNames(m.Examples),
		Obsolete:     isDeprecated(m.Doc),
		SourceURL:    sourceLinkOf(c.opts, c.fset, m.Decl),
	}
}

// valueRows builds one row per NAME in a constant or variable block.
//
// A doc.Value can carry several names declared together — `const ( A = 1; B = 2 )`
// is one Value with two names — and a reader searching for B must find it, so
// the block's shared signature is repeated on each row rather than the block
// being indexed once under its first name.
func (c *rowContext) valueRows(values []*doc.Value, kind string) []symbol {
	var rows []symbol
	//: one block at a time; each may declare several names.
	for _, v := range values {
		signature := signatureOf(c.fset, v.Decl)
		obsolete := isDeprecated(v.Doc)
		summary := synopsis(v.Doc)
		link := sourceLinkOf(c.opts, c.fset, v.Decl)
		//: one row per declared name, sharing the block's rendered signature.
		for _, n := range v.Names {
			rows = append(rows, symbol{
				Kind:         kind,
				Name:         n,
				Qualified:    c.pkgLabel + "." + n,
				Package:      c.pkg.ImportPath,
				PackageShort: c.packageShort,
				Signature:    signature,
				Doc:          summary,
				URL:          c.pkgURL + "#" + n,
				Obsolete:     obsolete,
				SourceURL:    link,
			})
		}
	}
	//: every name the blocks declared.
	return rows
}
