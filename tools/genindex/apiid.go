// Package main — the go: id of an exported symbol, the Go runtime's name of it.
package main

import (
	"fmt"
	"go/types"
	"regexp"
	"strings"
)

const (
	// idPrefix opens every id.
	idPrefix string = "go:"
	// genericMark follows a generic function, method or receiver type, as the
	// runtime writes an instantiation.
	genericMark string = "[...]"
	// escapedDot is how the linker writes a dot of an import path's last
	// element in a symbol's name.
	escapedDot string = "%2e"
)

// idFlags are the three marks an id can carry besides its names.
type idFlags uint8

const (
	// idPointer marks a method of a pointer receiver: (*T).M.
	idPointer idFlags = 1 << iota
	// idRecvGeneric marks a method whose receiver's type is generic: T[...].M.
	idRecvGeneric
	// idGeneric marks a generic function or method: F[...].
	idGeneric
)

// goIDPattern is the grammar of an id, the same expression platform's design
// schema calls goid: an import path whose last element writes its dots as
// %2e, then a name, a Type.Method or a (*Type).Method, [...] after a generic
// function, method or receiver type. docs/api/schema.json carries it with the
// vectors both sides test.
var goIDPattern = regexp.MustCompile(`^go:(?:[A-Za-z0-9_~+-](?:[A-Za-z0-9._~+-]*[A-Za-z0-9_~+-])?/)*` +
	`[A-Za-z0-9_~+-](?:(?:[A-Za-z0-9_~+-]|%2e)*[A-Za-z0-9_~+-])?` +
	`\.(?:\(\*[A-Z][A-Za-z0-9_]*(?:\[\.\.\.\])?\)\.|[A-Z][A-Za-z0-9_]*(?:\[\.\.\.\])?\.)?` +
	`[A-Z][A-Za-z0-9_]*(?:\[\.\.\.\])?$`)

// goID is an id's parts: the package, the receiver of a method, the name.
type goID struct {
	// path is the package's import path, unescaped.
	path string
	// recv is a method's type, or an interface's; empty for a package-level
	// symbol.
	recv string
	// name is the symbol's name: the method's, for a method.
	name string
	// flags are the id's marks.
	flags idFlags
}

// has reports whether the id carries a mark.
func (id goID) has(f idFlags) bool {
	//: the mark's bit.
	return id.flags&f != 0
}

// String writes the id as the runtime names the symbol.
func (id goID) String() string {
	var b strings.Builder
	b.WriteString(idPrefix)
	b.WriteString(escapePath(id.path))
	b.WriteByte('.')
	//: a method is written under its receiver's type.
	if id.recv != "" {
		b.WriteString(recvSpelling(id))
		b.WriteByte('.')
	}
	b.WriteString(id.name)
	//: a generic function or method carries the mark after its name.
	if id.has(idGeneric) {
		b.WriteString(genericMark)
	}
	//: the whole id.
	return b.String()
}

// recvSpelling writes a method's receiver as an id does: T, T[...], (*T) or
// (*T[...]).
func recvSpelling(id goID) string {
	recv := id.recv
	//: a generic receiver's type carries the mark inside the parentheses.
	if id.has(idRecvGeneric) {
		recv += genericMark
	}
	//: a pointer receiver is parenthesised.
	if id.has(idPointer) {
		//: (*T) or (*T[...]).
		return "(*" + recv + ")"
	}
	//: T or T[...].
	return recv
}

// escapePath writes an import path as the linker does in a symbol's name: a
// dot of the last element as %2e, so the path ends at the first dot after its
// last slash.
func escapePath(p string) string {
	slash := strings.LastIndexByte(p, '/')
	//: only the last element is escaped.
	return p[:slash+1] + strings.ReplaceAll(p[slash+1:], ".", escapedDot)
}

// unescapePath reads what escapePath wrote.
func unescapePath(p string) string {
	slash := strings.LastIndexByte(p, '/')
	//: only the last element was escaped.
	return p[:slash+1] + strings.ReplaceAll(p[slash+1:], escapedDot, ".")
}

// parseGoID reads an id, accepting exactly what goIDPattern accepts.
func parseGoID(s string) (goID, error) {
	//: the grammar first: anything else is no id.
	if !goIDPattern.MatchString(s) {
		//: quote it so a stray space is visible.
		return goID{}, fmt.Errorf("%q is no go: id", s)
	}
	rest := strings.TrimPrefix(s, idPrefix)
	slash := strings.LastIndexByte(rest, '/')
	dot := slash + 1 + strings.IndexByte(rest[slash+1:], '.')
	id := goID{path: unescapePath(rest[:dot])}
	member := splitRecv(&id, rest[dot+1:])
	recv, recvGeneric := strings.CutSuffix(id.recv, genericMark)
	name, generic := strings.CutSuffix(member, genericMark)
	id.recv, id.name = recv, name
	id.flags |= flagIf(recvGeneric, idRecvGeneric) | flagIf(generic, idGeneric)
	//: the parts.
	return id, nil
}

// flagIf is f when set holds, and no mark otherwise.
func flagIf(set bool, f idFlags) idFlags {
	//: the mark, or none.
	if set {
		//: the mark.
		return f
	}
	//: none.
	return 0
}

// splitRecv separates a method's receiver from its name, filling id's
// receiver and pointer mark, and returns what is left: the name.
func splitRecv(id *goID, member string) string {
	//: (*T).M or (*T[...]).M.
	if recv, ok := strings.CutPrefix(member, "(*"); ok {
		typ, name, _ := strings.Cut(recv, ").")
		id.recv = typ
		id.flags |= idPointer
		//: after the closing parenthesis and its dot.
		return name
	}
	//: T[...].M: the receiver's mark is followed by a dot.
	if typ, name, ok := strings.Cut(member, genericMark+"."); ok {
		id.recv = typ + genericMark
		//: after the mark's dot.
		return name
	}
	//: T.M, or a bare name (F or F[...], which holds no dot before its end).
	if recv, name, ok := strings.Cut(member, "."); ok && !strings.HasPrefix(name, "..") {
		id.recv = recv
		//: the method's name.
		return name
	}
	//: a package-level symbol.
	return member
}

// objectID is the id of a package-level object or a method, named by its
// origin when it is generic. ok is false for an object no id names: a local,
// a field, a method of an unnamed type, a predeclared object.
func objectID(obj types.Object) (id string, ok bool) {
	//: a predeclared object belongs to no package.
	if obj == nil || obj.Pkg() == nil {
		//: no id.
		return "", false
	}
	//: the forms an id can name.
	switch o := obj.(type) {
	//: a function or a method.
	case *types.Func:
		//: its runtime name.
		return funcID(o)
	//: a package-level variable, not a field.
	case *types.Var:
		//: a field or a local has no id.
		if o.IsField() || o.Parent() != o.Pkg().Scope() {
			//: no id.
			return "", false
		}
	//: a constant or a type name.
	case *types.Const, *types.TypeName:
		//: a local declaration has no id.
		if obj.Parent() != obj.Pkg().Scope() {
			//: no id.
			return "", false
		}
	//: a package name, a label, a builtin.
	default:
		//: no id.
		return "", false
	}
	//: go:<path>.<Name>.
	return goID{path: obj.Pkg().Path(), name: obj.Name()}.String(), true
}

// funcID is the id of a function or a method: [...] after a generic one, a
// method under its receiver's origin type.
func funcID(fn *types.Func) (id string, ok bool) {
	fn = fn.Origin()
	sig := fn.Signature()
	parts := goID{path: fn.Pkg().Path(), name: fn.Name(), flags: flagIf(sig.TypeParams().Len() > 0, idGeneric)}
	//: a method is named under its receiver's type.
	if sig.Recv() != nil {
		//: a receiver no named type defines names nothing.
		if !methodRecv(&parts, sig.Recv().Type()) {
			//: no id.
			return "", false
		}
		//: go:<path>.<Type>.<Method> or go:<path>.(*<Type>).<Method>.
		return parts.String(), true
	}
	//: a function declared in a body is a local.
	if fn.Parent() != nil && fn.Parent() != fn.Pkg().Scope() {
		//: no id.
		return "", false
	}
	//: go:<path>.<Name>.
	return parts.String(), true
}

// methodRecv fills an id's receiver from a method's receiver type: the
// origin's name, a pointer or not, generic or not. It reports false for a
// receiver that is no named type, an interface's method of an unnamed
// interface among them.
func methodRecv(id *goID, t types.Type) bool {
	t = types.Unalias(t)
	//: a pointer receiver.
	if p, ok := t.(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
		id.flags |= idPointer
	}
	named, ok := t.(*types.Named)
	//: only a named type's method has a runtime name.
	if !ok {
		//: no receiver to name.
		return false
	}
	id.recv = named.Origin().Obj().Name()
	id.flags |= flagIf(named.Origin().TypeParams().Len() > 0, idRecvGeneric)
	//: the receiver is named.
	return true
}
