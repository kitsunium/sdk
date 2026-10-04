// Package main — one checked package's exported API on one cell: every
// exported symbol with its id, kind, signatures, doc, file and code.
package main

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// kindFunc is a package-level function.
	kindFunc string = "func"
	// kindMethod is a method of an exported type, or an exported method of an
	// exported interface.
	kindMethod string = "method"
	// kindType is a defined type.
	kindType string = "type"
	// kindAlias is a type alias.
	kindAlias string = "alias"
	// kindConst is a constant.
	kindConst string = "const"
	// kindVar is a package-level variable.
	kindVar string = "var"
	// errsPath is the package whose Define makes a sentinel and whose Code a
	// constant is a dotted-quad error code of (ADR 0005).
	errsPath string = "github.com/kitsunium/sdk/internal/kernel/errs"
	// defineArgs is how many leading arguments of errs.Define a code reads:
	// the code, the reason and the public text.
	defineArgs int = 3
	// quadShift is the width of one element of a dotted quad, in bits.
	quadShift int = 8
	// quadTop is the shift of a dotted quad's first element.
	quadTop int = 24
	// quadMask selects one element of a dotted quad.
	quadMask uint64 = 0xff
	// decimal is the base a dotted quad writes its elements in.
	decimal int = 10
)

// pathQualifier writes every package as its import path, the declaring
// package's own included: the canonical spelling.
func pathQualifier(p *types.Package) string {
	//: the import path, always.
	return p.Path()
}

// apiSymbol is one exported symbol as docs/api records it.
type apiSymbol struct {
	// ID is the symbol's go: id.
	ID string `json:"id"`
	// Kind is func, method, type, alias, const or var.
	Kind string `json:"kind"`
	// Package is the declaring package's import path.
	Package string `json:"package"`
	// Name is the symbol's name; a method's own name for a method.
	Name string `json:"name"`
	// Recv is a method's type, without its type parameters.
	Recv string `json:"recv,omitempty"`
	// Spelled is the signature as the declaring file writes its types.
	Spelled string `json:"spelled"`
	// Canonical is the signature with every package by its import path.
	Canonical string `json:"canonical"`
	// Owner is the id of the type an alias resolves to, when it is named.
	Owner string `json:"owner,omitempty"`
	// Value is a constant's exact value.
	Value string `json:"value,omitempty"`
	// Init is the id a variable or constant is initialised with, when its
	// initializer is that one name.
	Init string `json:"init,omitempty"`
	// Code is an error code's, or an errs sentinel's.
	Code *apiCode `json:"code,omitempty"`
	// Fields are a struct type's exported fields.
	Fields []apiField `json:"fields,omitempty"`
	// Doc is the doc comment's text.
	Doc string `json:"doc,omitempty"`
	// File is the declaring file, relative to the module's root.
	File string `json:"file"`
	// Layer is the declaring package's layer.
	Layer string `json:"layer,omitempty"`
	// Family is the declaring package's family within its layer.
	Family string `json:"family,omitempty"`
	// Platforms are the cells this record holds on; absent when it holds on
	// every cell of the document.
	Platforms []string `json:"platforms,omitempty"`
}

// apiCode is a dotted-quad error code, with an errs sentinel's reason and
// wire-safe public text.
type apiCode struct {
	// Value is the code as MM.LL.PP.SS.
	Value string `json:"value"`
	// Reason is a sentinel's reason.
	Reason string `json:"reason,omitempty"`
	// Public is a sentinel's public text.
	Public string `json:"public,omitempty"`
}

// apiField is one exported field of a struct type.
type apiField struct {
	// Name is the field's name; an embedded field's type name.
	Name string `json:"name"`
	// Embedded is set for an embedded field.
	Embedded bool `json:"embedded,omitempty"`
	// Type is the field's type as the declaring file writes it.
	Type string `json:"type"`
	// Tag is the field's struct tag.
	Tag string `json:"tag,omitempty"`
	// Doc is the field's doc comment, or its line comment when it has none.
	Doc string `json:"doc,omitempty"`
}

// pkgReader reads one checked package's exported symbols on one cell.
type pkgReader struct {
	// pkg is the checked package.
	pkg *types.Package
	// info holds the definitions, uses and expression types of its files.
	info *types.Info
	// files are its files on this cell, by name.
	files []*ast.File
	// modDir is its module's directory, which files are made relative to.
	modDir string
	// place is the package's layer and family.
	place pkgPlace
	// codes are the cell's sentinel codes by id, read from the packages
	// already read.
	codes map[string]*apiCode
	// out are the symbols read so far.
	out []apiSymbol
	// spell writes a type as the file being read does.
	spell types.Qualifier
	// file is the file being read, relative to the module.
	file string
}

// readSymbols returns every exported symbol of the package, in source order,
// and records its sentinels' codes for the packages read after it.
func (r *pkgReader) readSymbols(fset *token.FileSet) []apiSymbol {
	//: each file, in name order.
	for _, f := range r.files {
		r.spell = r.qualifier(f)
		r.file = relSlash(r.modDir, fset.Position(f.Package).Filename)
		//: each declaration of the file.
		for _, d := range f.Decls {
			r.decl(d)
		}
	}
	r.resolveCodes()
	//: the package's symbols.
	return r.out
}

// decl reads one top-level declaration.
func (r *pkgReader) decl(d ast.Decl) {
	//: the two kinds of declaration a package's scope holds.
	switch d := d.(type) {
	//: a function or a method.
	case *ast.FuncDecl:
		r.funcDecl(d)
	//: a const, var, type or import declaration.
	case *ast.GenDecl:
		//: each spec of the group.
		for _, spec := range d.Specs {
			r.spec(d, spec)
		}
	//: a declaration the parser could not read declares nothing.
	default:
	}
}

// spec reads one spec of a declaration group.
func (r *pkgReader) spec(d *ast.GenDecl, spec ast.Spec) {
	//: a value or a type; an import declares nothing exported.
	switch s := spec.(type) {
	//: constants and variables.
	case *ast.ValueSpec:
		r.valueSpec(d, s)
	//: a type or an alias.
	case *ast.TypeSpec:
		r.typeSpec(d, s)
	//: an import.
	default:
	}
}

// qualifier is how file f spells another package: the name it imports it
// under, nothing for a dot import or the package itself, the package's name
// when the file does not import it.
func (r *pkgReader) qualifier(f *ast.File) types.Qualifier {
	names := r.importNames(f)
	self := r.pkg
	//: the file's spelling.
	return func(p *types.Package) string {
		//: the package's own names are unqualified.
		if p == self {
			//: no qualifier.
			return ""
		}
		name, imported := names[p.Path()]
		//: a package the file does not import is written by its name.
		if !imported {
			//: the package's name.
			return p.Name()
		}
		//: a dot import is unqualified.
		if name == "." {
			//: no qualifier.
			return ""
		}
		//: the import name.
		return name
	}
}

// importNames maps each package a file imports to the first name it imports
// it under, a dot import overridden by a named one.
func (r *pkgReader) importNames(f *ast.File) map[string]string {
	names := map[string]string{}
	//: every import of the file.
	for _, spec := range f.Imports {
		pn := r.info.PkgNameOf(spec)
		//: a blank import names nothing.
		if pn == nil || pn.Name() == "_" {
			continue
		}
		//: the first name wins, unless it was a dot import.
		if prev, seen := names[pn.Imported().Path()]; !seen || prev == "." {
			names[pn.Imported().Path()] = pn.Name()
		}
	}
	//: the names.
	return names
}

// both writes a type as the file spells it and canonically.
func (r *pkgReader) both(t types.Type) (spelled, canonical string) {
	//: one type, two qualifiers.
	return types.TypeString(t, r.spell), types.TypeString(t, pathQualifier)
}

// symbol starts a record of the package for the file being read.
func (r *pkgReader) symbol(kind string, obj types.Object, doc *ast.CommentGroup) apiSymbol {
	id, _ := objectID(obj)
	//: the fields every kind carries.
	return apiSymbol{
		ID: id, Kind: kind, Package: r.pkg.Path(), Name: obj.Name(), Doc: doc.Text(),
		File: r.file, Layer: r.place.layer, Family: r.place.family,
	}
}

// funcDecl reads an exported function, or an exported method of an exported
// type.
func (r *pkgReader) funcDecl(d *ast.FuncDecl) {
	fn, _ := r.info.Defs[d.Name].(*types.Func)
	//: an unexported or unresolved function is no API.
	if fn == nil || !fn.Exported() {
		//: nothing to record.
		return
	}
	kind, recv := kindFunc, ""
	//: a method belongs to its receiver's type, which must be exported too.
	if d.Recv != nil {
		var id goID
		//: a receiver no exported named type defines is no API.
		if !methodRecv(&id, fn.Signature().Recv().Type()) || !token.IsExported(id.recv) {
			//: nothing to record.
			return
		}
		kind, recv = kindMethod, id.recv
	}
	s := r.symbol(kind, fn, d.Doc)
	s.Recv = recv
	s.Spelled, s.Canonical = r.both(fn.Signature())
	r.out = append(r.out, s)
}

// valueSpec reads the exported constants and variables of one spec.
func (r *pkgReader) valueSpec(d *ast.GenDecl, s *ast.ValueSpec) {
	doc := specDoc(d, s.Doc)
	//: one record per exported name.
	for i, name := range s.Names {
		//: an unexported name is no API.
		if !name.IsExported() {
			continue
		}
		var init ast.Expr
		//: an initializer per name: name i is initialised by expression i.
		if len(s.Values) == len(s.Names) {
			init = s.Values[i]
		}
		//: the object the name defines.
		switch o := r.info.Defs[name].(type) {
		//: a constant.
		case *types.Const:
			r.out = append(r.out, r.constSymbol(o, init, doc))
		//: a variable.
		case *types.Var:
			r.out = append(r.out, r.varSymbol(o, init, doc))
		//: an unresolved name.
		default:
		}
	}
}

// constSymbol records a constant: its type, its exact value, the name it is
// initialised with, and its dotted quad when it is an error code.
func (r *pkgReader) constSymbol(o *types.Const, init ast.Expr, doc *ast.CommentGroup) apiSymbol {
	s := r.symbol(kindConst, o, doc)
	s.Spelled, s.Canonical = r.both(o.Type())
	s.Value = o.Val().ExactString()
	s.Init, _ = r.initID(init)
	//: an errs.Code constant is an error code.
	if isErrsCode(o.Type()) {
		//: its value, as a dotted quad.
		if v, exact := constant.Uint64Val(constant.ToInt(o.Val())); exact {
			s.Code = &apiCode{Value: dottedQuad(v)}
		}
	}
	//: the constant.
	return s
}

// varSymbol records a variable: its type, the name it is initialised with,
// and an errs sentinel's code.
func (r *pkgReader) varSymbol(o *types.Var, init ast.Expr, doc *ast.CommentGroup) apiSymbol {
	s := r.symbol(kindVar, o, doc)
	s.Spelled, s.Canonical = r.both(o.Type())
	s.Init, _ = r.initID(init)
	s.Code = r.defineCode(init)
	//: a sentinel's code is available to the packages read after this one.
	if s.Code != nil {
		r.codes[s.ID] = s.Code
	}
	//: the variable.
	return s
}

// initID is the id an initializer names when it is one name: a constant, a
// variable or a function of a package's scope. ok is false for anything
// else.
func (r *pkgReader) initID(init ast.Expr) (id string, ok bool) {
	var ident *ast.Ident
	//: the two spellings of one name.
	switch e := ast.Unparen(init).(type) {
	//: a name of the package's own scope.
	case *ast.Ident:
		ident = e
	//: a name of an imported package.
	case *ast.SelectorExpr:
		ident = e.Sel
	//: anything else is an expression, not a name.
	default:
		//: no id.
		return "", false
	}
	//: the object the name refers to, by its id.
	return objectID(r.info.Uses[ident])
}

// resolveCodes gives a variable initialised with a sentinel the sentinel's
// code, until no record changes: a re-export of a re-export within the
// package resolves too.
func (r *pkgReader) resolveCodes() {
	//: a chain of n re-exports resolves in at most n passes.
	for changed := true; changed; {
		changed = false
		//: every variable still without a code.
		for i := range r.out {
			s := &r.out[i]
			//: only a variable named after another can inherit a code.
			if s.Kind != kindVar || s.Code != nil || s.Init == "" {
				continue
			}
			//: the code of the variable it names, when it has one.
			if code, ok := r.codes[s.Init]; ok {
				s.Code = code
				r.codes[s.ID] = code
				changed = true
			}
		}
	}
}

// defineCode reads the code, the reason and the public text of a call to
// errs.Define, or nil when the initializer is no such call or an argument is
// no constant.
func (r *pkgReader) defineCode(init ast.Expr) *apiCode {
	call, ok := ast.Unparen(init).(*ast.CallExpr)
	//: only a call to errs.Define defines a sentinel.
	if !ok || len(call.Args) < defineArgs || !isDefine(r.calledObject(call)) {
		//: no code.
		return nil
	}
	code := r.info.Types[call.Args[0]].Value
	reason := r.info.Types[call.Args[1]].Value
	public := r.info.Types[call.Args[2]].Value
	//: every argument read must be a constant of its kind.
	if code == nil || reason == nil || public == nil || reason.Kind() != constant.String || public.Kind() != constant.String {
		//: no code.
		return nil
	}
	v, exact := constant.Uint64Val(constant.ToInt(code))
	//: a code is a 32-bit dotted quad.
	if !exact {
		//: no code.
		return nil
	}
	//: the sentinel's code.
	return &apiCode{Value: dottedQuad(v), Reason: constant.StringVal(reason), Public: constant.StringVal(public)}
}

// calledObject is the object a call's function expression names, or nil.
func (r *pkgReader) calledObject(call *ast.CallExpr) types.Object {
	//: a qualified or a plain name.
	switch fun := ast.Unparen(call.Fun).(type) {
	//: pkg.Define.
	case *ast.SelectorExpr:
		//: what Define resolves to.
		return r.info.Uses[fun.Sel]
	//: Define, dot-imported or declared here.
	case *ast.Ident:
		//: what the name resolves to.
		return r.info.Uses[fun]
	//: a call of any other expression.
	default:
		//: no named function.
		return nil
	}
}

// isDefine reports whether obj is errs.Define.
func isDefine(obj types.Object) bool {
	fn, ok := obj.(*types.Func)
	//: the function, by package and name.
	return ok && fn.Pkg() != nil && fn.Pkg().Path() == errsPath && fn.Name() == "Define"
}

// isErrsCode reports whether t is errs.Code, through any alias of it.
func isErrsCode(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	//: the defined type, by package and name.
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == errsPath && named.Obj().Name() == "Code"
}

// dottedQuad writes a code as MM.LL.PP.SS (ADR 0005).
func dottedQuad(v uint64) string {
	parts := make([]string, 0, quadTop/quadShift+1)
	//: from the most significant byte down.
	for shift := quadTop; shift >= 0; shift -= quadShift {
		parts = append(parts, strconv.FormatUint(v>>uint(shift)&quadMask, decimal))
	}
	//: four decimal bytes, dot-separated.
	return strings.Join(parts, ".")
}

// relSlash is name relative to dir, with forward slashes; name itself when it
// is not under dir.
func relSlash(dir, name string) string {
	rel, err := filepath.Rel(dir, name)
	//: a file outside the module is named as it is.
	if err != nil {
		//: unchanged, in slashes.
		return filepath.ToSlash(name)
	}
	//: relative, in slashes.
	return filepath.ToSlash(rel)
}
