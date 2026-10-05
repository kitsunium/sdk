package jsonshape

import (
	"reflect"
)

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

// marshalText is Kind.MarshalText's body: decl_gen.go writes Kind.MarshalText, from the
// design, as one call of it.
func (k Kind) marshalText() ([]byte, error) {
	//: the name, as text.
	return []byte(k.String()), nil
}

// of is Of's body: decl_gen.go writes Of, from the
// design, as one call of it.
func of(t reflect.Type) *ShapeValue {
	//: a fresh walk: nothing on its path yet.
	return newWalker().shape(t, false)
}

// For describes the values of T on the wire: Of(reflect.TypeFor[T]()).
func For[T any]() *ShapeValue {
	//: the type argument's reflect.Type.
	return Of(reflect.TypeFor[T]())
}
