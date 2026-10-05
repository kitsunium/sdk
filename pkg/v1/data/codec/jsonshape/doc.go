// Package jsonshape describes how values of a Go type look on the wire under
// encoding/json — the JSON kind each value takes, the members an object has
// and under which names, which may be missing and which may be null — for a
// reader to show: an API's documentation, a developer console, a schema
// generator (ADR 0133).
//
// # The whole thing
//
//	shape := jsonshape.For[Order]()
//	for _, field := range shape.Fields {
//		fmt.Println(field.Name, field.Shape.Kind, field.Optional)
//	}
//
// # What it follows
//
// The description follows the encoding/json of the toolchain the SDK is built
// with — Go 1.27, whose encoding/json runs on the json/v2 engine with the v1
// options — and describes ENCODING: encoding/json decodes any member as
// absent, so [Field].Optional says what an encoded value may omit, never what
// a decoder requires.
//
// Struct members are resolved exactly as encoding/json resolves them:
// embedded structs promoted one level at a time, through a pointer, an
// unexported embedded struct's exported fields included; for one name, the
// shallowest field wins, a single tagged one breaks a tie at that depth, and a
// tie that remains writes neither — nor any deeper field of that name. A
// field promoted through an embedded pointer is Optional, since a nil one
// drops it. Tag names are read as the engine reads them, and json/v2's embed
// option is honoured: a named struct field carrying it is promoted, and an
// embedded map takes the members no field names ([Shape].Values on an
// Object).
//
// # What a type decides for itself
//
// A type that writes its own JSON — json.Marshaler, or json/v2's MarshalerTo
// — is opaque: [Any], whatever it writes. One that writes text — an
// encoding.TextMarshaler or TextAppender — is a [String]. A struct that
// embeds time.Time gains its MarshalJSON and is opaque too.
//
// A method with a POINTER receiver counts only where encoding/json calls it:
// on an addressable value. [Of](T) describes a T encoded by value —
// json.Marshal(v) — whose root is not addressable, and neither are the fields
// and array elements under it; a pointer's and a slice's elements are, and a
// map's values never are. For json.Marshal(&v), describe *T.
//
// Three types are described by what is written rather than by their kind:
// time.Time is a String with Format "date-time", time.Duration an Integer
// with Format "duration-ns", json.Number a Number.
//
// A nil pointer, slice, map or interface is written as null, so their shapes
// are Nullable; an array never is. A []byte is a base64 String; a byte ARRAY
// is an Array of integers. A map whose key encoding/json cannot write as a
// member name — a bool, a struct — is [Unsupported], as are channels,
// functions and complex numbers: encoding/json refuses the whole value.
//
// # Recursion
//
// A type that reaches itself is described once; further down, its shape is a
// reference ([Shape].Ref, the type's qualified name) with no members. A type
// used twice side by side is described twice.
//
// # The Go field behind each member
//
// [Field].Tag, [Field].GoName and [Field].Index give access to the Go field a
// member is written from, promoted fields included: a framework can read its
// own tags — where a request field is read from, whether it is secret —
// beside encoding/json's, and reach the value with reflect.Value.FieldByIndex.
// A field encoding/json never writes (json:"-", unexported) has no member; a
// framework that needs it finds it with reflect.VisibleFields and matches the
// two by index path.
package jsonshape
