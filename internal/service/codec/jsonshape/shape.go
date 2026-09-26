// Package jsonshape describes how values of a Go type look on the wire under
// encoding/json: which JSON kind each value takes, which members an object
// has and under which names, which may be missing and which may be null
// (ADR 0133).
//
// The description follows the encoding/json of the toolchain the SDK is built
// with — Go 1.27, whose encoding/json runs on the json/v2 engine with the v1
// options — and it is a description of ENCODING: encoding/json decodes any
// member as absent, so Optional says what an encoded value may omit, never
// what a decoder requires.
//
// Struct members are resolved as encoding/json resolves them: embedded
// structs promoted one level at a time, through a pointer, an unexported
// embedded struct's exported fields included; for one name, the shallowest
// field wins, a single tagged one breaks a tie at that depth, and a tie that
// remains writes neither — nor any deeper field of that name. A field keeps
// access to the Go field behind it: its struct tag, its Go name, and its index
// path for reflect.Value.FieldByIndex, so a framework can read its own tags
// beside encoding/json's.
//
// A type that writes its own JSON — json.Marshaler, or json/v2's MarshalerTo —
// is opaque: Any, whatever it writes. One that writes text — an
// encoding.TextMarshaler or TextAppender — is a String. A pointer receiver's
// method counts only where encoding/json calls it: on an addressable value.
// Of(T) describes a T encoded by value — json.Marshal(v) — whose root is not
// addressable, and neither are the fields and array elements under it; a
// pointer's and a slice's elements are, and a map's values never are. Describe
// *T for json.Marshal(&v).
// time.Time is described by what its method writes — a date-time string — and
// json.Number by what encoding/json writes for it — a number.
package jsonshape

import (
	"reflect"
)

// Kind is the JSON kind a value takes on the wire. The zero Kind is Any.
type Kind uint8

const (
	// Any is a value whose kind the type does not decide: an interface, a
	// json.RawMessage, a type that writes its own JSON.
	Any Kind = iota
	// Object is a JSON object whose members are Fields: a struct.
	Object
	// Array is a JSON array whose elements are Items: a slice or an array.
	Array
	// Map is a JSON object whose member names are keys and whose values are
	// Values: a Go map.
	Map
	// String is a JSON string: a string, a []byte in base64, a text
	// marshaler, a time.Time.
	String
	// Integer is a JSON number the type holds as an integer.
	Integer
	// Number is a JSON number the type holds as a float, or json.Number.
	Number
	// Boolean is true or false.
	Boolean
	// Unsupported is a type encoding/json refuses to encode: a channel, a
	// function, a complex number, an unsafe pointer, a map whose key it
	// cannot write as a member name. A value holding one is refused whole.
	Unsupported
)

// kindNames are the Kind spellings, indexed by Kind.
var kindNames = [...]string{"any", "object", "array", "map", "string", "integer", "number", "boolean", "unsupported"}

// String returns the kind's lower-case name: "object", "integer", "any".
func (k Kind) String() string {
	//: a Kind this package never mints.
	if int(k) >= len(kindNames) {
		return "unknown"
	}
	return kindNames[k]
}

// MarshalText writes the kind's name, so a shape encodes as readable JSON.
func (k Kind) MarshalText() ([]byte, error) {
	//: the name, as text.
	return []byte(k.String()), nil
}

// ShapeValue describes the values of one Go type on the wire: the JSON kind
// they take and, for a container, the shape of what it holds. It encodes as
// readable JSON; the Go-only parts of its fields are left out.
type ShapeValue struct {
	// Kind is the JSON kind the values take.
	Kind Kind `json:"kind"`
	// Name is the qualified Go type name — "time.Time",
	// "example.com/shop.Order" — for a named type from a package, and empty
	// otherwise.
	Name string `json:"name,omitempty"`
	// Format refines a kind: the Go kind of a number ("int64", "uint8",
	// "float32"), "base64" for bytes written as a string, "date-time" for a
	// time.Time, "duration-ns" for a time.Duration's integer nanoseconds.
	Format string `json:"format,omitempty"`
	// Nullable reports that a value of the type can be written as null: a
	// nil pointer, slice, map or interface.
	Nullable bool `json:"nullable,omitempty"`
	// Fields are an Object's members, in the order encoding/json writes them.
	Fields []FieldValue `json:"fields,omitempty"`
	// Items is an Array's element shape.
	Items *ShapeValue `json:"items,omitempty"`
	// Values is a Map's value shape — and, on an Object, the shape of the
	// members an embedded map adds beyond Fields (json/v2's embed option).
	Values *ShapeValue `json:"values,omitempty"`
	// Ref is set, to Name, on the shape of a type already being described
	// further up: the description stops there instead of recursing. A
	// recursive type is always a named one, so Ref names an ancestor.
	Ref string `json:"ref,omitempty"`
}

// FieldValue is one member of an Object: its wire name and shape, what the
// encoder may do with it, and the Go field it is written from — which a
// framework reads its own tags from.
type FieldValue struct {
	// Name is the member name on the wire.
	Name string `json:"name"`
	// Shape describes the member's value. When Quoted, the value travels
	// inside a JSON string.
	Shape *ShapeValue `json:"shape"`
	// Optional reports that an encoded object may lack the member: its tag
	// says omitempty on a kind that can be empty, or omitzero, or the field
	// is promoted through an embedded pointer that may be nil.
	Optional bool `json:"optional,omitempty"`
	// Quoted reports the ",string" option taking effect: the value is written
	// as a JSON string holding its own encoding — "42" for 42.
	Quoted bool `json:"quoted,omitempty"`
	// Rules is the field's validate tag as written, for a reader to show.
	Rules string `json:"rules,omitempty"`
	// GoName is the Go field's name.
	GoName string `json:"-"`
	// Tag is the Go field's whole struct tag, so a framework can read tags of
	// its own — where a request field is read from — beside encoding/json's.
	Tag reflect.StructTag `json:"-"`
	// Index is the path to the Go field from the described struct, through
	// every embedding, for reflect.Type.FieldByIndex and
	// reflect.Value.FieldByIndex.
	Index []int `json:"-"`
}

// Of describes the values of t on the wire. A nil t — the type of a nil
// interface — is Any. Of never fails: what encoding/json refuses is described
// as Unsupported where it occurs. Every call returns a new tree, which the
// caller may keep and change.
func Of(t reflect.Type) *ShapeValue {
	//: a fresh walk: nothing on its path yet.
	return newWalker().shape(t, false)
}

// For describes the values of T on the wire: Of(reflect.TypeFor[T]()).
func For[T any]() *ShapeValue {
	//: the type argument's reflect.Type.
	return Of(reflect.TypeFor[T]())
}
