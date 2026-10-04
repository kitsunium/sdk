// Package main — the types of a checked package's API: defined types, their
// exported fields, interfaces' exported methods, and aliases with the named
// type each resolves to.
package main

import (
	"cmp"
	"go/ast"
	"go/types"
	"strings"
)

// typeSpec reads an exported type or alias, and an interface's exported
// methods.
func (r *pkgReader) typeSpec(d *ast.GenDecl, s *ast.TypeSpec) {
	tn, _ := r.info.Defs[s.Name].(*types.TypeName)
	//: an unexported or unresolved type is no API.
	if tn == nil || !tn.Exported() {
		//: nothing to record.
		return
	}
	//: an alias records its right-hand side and what it resolves to.
	if a, ok := tn.Type().(*types.Alias); ok {
		r.out = append(r.out, r.aliasSymbol(tn, a, specDoc(d, s.Doc)))
		//: an alias declares no member of its own.
		return
	}
	named, ok := tn.Type().(*types.Named)
	//: a type name that is neither is not a declaration this reads.
	if !ok {
		//: nothing to record.
		return
	}
	r.out = append(r.out, r.typeSymbol(tn, named, s, specDoc(d, s.Doc)))
	//: an interface's exported methods are symbols of their own.
	if it, isIface := named.Underlying().(*types.Interface); isIface {
		r.interfaceMethods(tn, it, s.Type)
	}
}

// aliasSymbol records an alias: its type parameters and right-hand side, and
// the id of the named type it resolves to.
func (r *pkgReader) aliasSymbol(tn *types.TypeName, a *types.Alias, doc *ast.CommentGroup) apiSymbol {
	s := r.symbol(kindAlias, tn, doc)
	rhsSpelled, rhsCanonical := r.both(a.Rhs())
	s.Spelled = withTypeParams(typeParamList(tn, r.spell), rhsSpelled)
	s.Canonical = withTypeParams(typeParamList(tn, pathQualifier), rhsCanonical)
	//: the owner is the origin of the named type the alias resolves to.
	if named, ok := types.Unalias(a).(*types.Named); ok {
		s.Owner, _ = objectID(named.Origin().Obj())
	}
	//: the alias.
	return s
}

// typeSymbol records a defined type: its type parameters and its underlying
// type, a struct's exported fields only.
func (r *pkgReader) typeSymbol(tn *types.TypeName, named *types.Named, s *ast.TypeSpec, doc *ast.CommentGroup) apiSymbol {
	sym := r.symbol(kindType, tn, doc)
	under := exportedOnly(named.Underlying())
	spelled, canonical := r.both(under)
	sym.Spelled = withTypeParams(typeParamList(tn, r.spell), spelled)
	sym.Canonical = withTypeParams(typeParamList(tn, pathQualifier), canonical)
	//: a struct's exported fields, with their docs.
	if st, ok := under.(*types.Struct); ok {
		sym.Fields = r.fields(st, s.Type)
	}
	//: the type.
	return sym
}

// interfaceMethods records each exported explicit method of an interface;
// an unexported one is spelled in the interface's own signature.
func (r *pkgReader) interfaceMethods(tn *types.TypeName, it *types.Interface, expr ast.Expr) {
	docs := memberDocs(expr)
	//: the explicit methods, in go/types' order; the document sorts them.
	for m := range it.ExplicitMethods() {
		//: an unexported method is no symbol of its own.
		if !m.Exported() {
			continue
		}
		s := r.symbol(kindMethod, m, nil)
		s.Doc = docs[m.Name()]
		s.Recv = tn.Name()
		s.Spelled, s.Canonical = r.both(m.Type())
		r.out = append(r.out, s)
	}
}

// fields records a struct's exported fields, with the docs the declaration
// writes for them.
func (r *pkgReader) fields(st *types.Struct, expr ast.Expr) []apiField {
	docs := memberDocs(expr)
	out := make([]apiField, 0, st.NumFields())
	//: every field the exported-only struct kept, with its tag.
	for i := range st.NumFields() {
		f := st.Field(i)
		out = append(out, apiField{
			Name: f.Name(), Embedded: f.Embedded(), Type: types.TypeString(f.Type(), r.spell),
			Tag: st.Tag(i), Doc: docs[f.Name()],
		})
	}
	//: the fields.
	return out
}

// exportedOnly is a struct with only its exported fields, and their tags; any
// other type unchanged. An unexported field is not part of the API a design
// declares, so it is not part of the signature either.
func exportedOnly(t types.Type) types.Type {
	st, ok := t.(*types.Struct)
	//: only a struct is filtered.
	if !ok {
		//: unchanged.
		return t
	}
	var fields []*types.Var
	var tags []string
	//: the exported fields, in order, with their tags.
	for i := range st.NumFields() {
		//: an unexported field is left out.
		if f := st.Field(i); f.Exported() {
			fields = append(fields, f)
			tags = append(tags, st.Tag(i))
		}
	}
	//: the exported struct.
	return types.NewStruct(fields, tags)
}

// typeParamList is a generic type's or alias's type parameter list as go/types
// writes it — [K comparable, V any] — and empty when it has none: what
// TypeString writes after the type's qualified name.
func typeParamList(tn *types.TypeName, q types.Qualifier) string {
	full := types.TypeString(tn.Type(), q)
	head := tn.Name()
	//: a qualified name, when the qualifier writes one for the package.
	if prefix := q(tn.Pkg()); prefix != "" {
		head = prefix + "." + head
	}
	//: whatever follows the name.
	return strings.TrimPrefix(full, head)
}

// withTypeParams writes a type parameter list before a type, separated by a
// space, as a generic declaration reads.
func withTypeParams(params, t string) string {
	//: a type with no type parameter is written alone.
	if params == "" {
		//: the type.
		return t
	}
	//: [K comparable] struct{…}.
	return params + " " + t
}

// memberDocs maps each member a struct or interface literal declares — a
// field, an embedded field by its type's name, a method — to its doc
// comment, or its line comment when it has none.
func memberDocs(expr ast.Expr) map[string]string {
	var list *ast.FieldList
	//: the literal that declares members.
	switch t := ast.Unparen(expr).(type) {
	//: a struct's fields.
	case *ast.StructType:
		list = t.Fields
	//: an interface's methods and embeds.
	case *ast.InterfaceType:
		list = t.Methods
	//: a named or composite type declares no member here.
	default:
		//: no doc.
		return nil
	}
	docs := map[string]string{}
	//: each member line.
	for _, f := range list.List {
		text := cmp.Or(f.Doc.Text(), f.Comment.Text())
		//: every name the line declares, or the embedded type's.
		for _, name := range memberNames(f) {
			docs[name] = text
		}
	}
	//: the docs by member name.
	return docs
}

// memberNames are the names one field line declares, or, for an embedded
// field, the name of its type.
func memberNames(f *ast.Field) []string {
	//: an embedded field is named after its type.
	if len(f.Names) == 0 {
		//: the type's own name, when it has one.
		if name, ok := embeddedName(f.Type); ok {
			//: that name.
			return []string{name}
		}
		//: an embedded type without a name names nothing.
		return nil
	}
	out := make([]string, 0, len(f.Names))
	//: each declared name.
	for _, n := range f.Names {
		out = append(out, n.Name)
	}
	//: the names.
	return out
}

// embeddedName is the name of an embedded field's type: T for T, *T, pkg.T
// and T[A]. ok is false for an expression that names no type.
func embeddedName(e ast.Expr) (name string, ok bool) {
	//: peel what wraps the name.
	for {
		//: each wrapper an embedded type can have.
		switch t := e.(type) {
		//: a pointer.
		case *ast.StarExpr:
			e = t.X
		//: parentheses.
		case *ast.ParenExpr:
			e = t.X
		//: an instantiation with one argument.
		case *ast.IndexExpr:
			e = t.X
		//: an instantiation with several.
		case *ast.IndexListExpr:
			e = t.X
		//: pkg.T.
		case *ast.SelectorExpr:
			//: the selected name.
			return t.Sel.Name, true
		//: T.
		case *ast.Ident:
			//: the name.
			return t.Name, true
		//: no name to take.
		default:
			//: nothing.
			return "", false
		}
	}
}

// specDoc is a spec's doc comment, or its group's when it has none.
func specDoc(d *ast.GenDecl, own *ast.CommentGroup) *ast.CommentGroup {
	//: the spec's own comment wins; the group's documents every spec it holds.
	return cmp.Or(own, d.Doc)
}
