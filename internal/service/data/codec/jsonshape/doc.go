// Package jsonshape — the members encoding/json writes for a struct, resolved
// by its own rules: json/v2's field walk under the v1 options Go 1.27's
// encoding/json runs with.
//
// Package jsonshape — which methods make encoding/json hand a type's encoding
// over to the type itself.
//
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
//
// Package jsonshape — a struct field's json tag, read with the grammar the
// json/v2 engine reads it with.
//
// Package jsonshape — the walk from a Go type to its shape.
package jsonshape
