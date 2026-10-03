// Package jsonshape — which methods make encoding/json hand a type's encoding
// over to the type itself.
package jsonshape

import (
	"encoding"
	"encoding/json"
	"reflect"
)

// jsontextPackage is the import path of the package json/v2's streaming
// methods take their encoder and decoder from.
const jsontextPackage string = "encoding/json/jsontext"

// The two streaming methods.
const (
	// marshalJSONTo is func(*jsontext.Encoder) error.
	marshalJSONTo streamMethod = iota
	// unmarshalJSONFrom is func(*jsontext.Decoder) error.
	unmarshalJSONFrom
)

// The interfaces encoding/json consults, by their reflect.Type.
var (
	jsonMarshalerType   = reflect.TypeFor[json.Marshaler]()
	jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
	textMarshalerType   = reflect.TypeFor[encoding.TextMarshaler]()
	textAppenderType    = reflect.TypeFor[encoding.TextAppender]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	isZeroerType        = reflect.TypeFor[interface{ IsZero() bool }]()
	errorType           = reflect.TypeFor[error]()
)

// streamMethod names one of json/v2's two streaming methods.
type streamMethod int

// receives reports whether encoding/json calls iface's method on a value of t:
// a value receiver's always, a pointer receiver's only when the value is
// addressable — the v1 semantics Go 1.27's encoding/json keeps, under which a
// value copied to be encoded (a root passed by value, a map's key or value)
// does not have its pointer methods called.
func receives(t, iface reflect.Type, addressable bool) bool {
	//: a value receiver, then a pointer receiver on an addressable value.
	return t.Implements(iface) || (addressable && reflect.PointerTo(t).Implements(iface))
}

// hasStreamMethod reports whether encoding/json calls json/v2's streaming
// method m on a value of t: by value always, by pointer only on an
// addressable value. It is matched by signature so this package does not
// import json/v2 to recognise it.
func hasStreamMethod(t reflect.Type, m streamMethod, addressable bool) bool {
	//: a value receiver, then a pointer receiver on an addressable value.
	return streamMethodOn(t, m) || (addressable && streamMethodOn(reflect.PointerTo(t), m))
}

// streamMethodOn reports whether the method set of receiver holds the
// streaming method m, with its signature. Each name is looked up as a
// constant: a MethodByName whose argument the compiler cannot see makes the
// linker keep every exported method of every type a program uses, which is
// how a product that imported this package linked all of the framework.
func streamMethodOn(receiver reflect.Type, m streamMethod) bool {
	var (
		method reflect.Method
		found  bool
		param  string
	)
	switch m {
	case marshalJSONTo:
		method, found = receiver.MethodByName("MarshalJSONTo")
		param = "Encoder"
	default:
		method, found = receiver.MethodByName("UnmarshalJSONFrom")
		param = "Decoder"
	}
	//: no such method on this receiver.
	if !found {
		return false
	}
	signature := method.Type
	//: the receiver, one argument, one error result.
	if signature.NumIn() != 2 || signature.NumOut() != 1 || signature.Out(0) != errorType {
		return false
	}
	argument := signature.In(1)
	//: a *jsontext.Encoder or a *jsontext.Decoder.
	return argument.Kind() == reflect.Pointer && argument.Elem().PkgPath() == jsontextPackage && argument.Elem().Name() == param
}

// writesJSON reports whether a value of t writes its own JSON —
// json.Marshaler, or json/v2's MarshalerTo — where it is encoded, addressable
// or not.
func writesJSON(t reflect.Type, addressable bool) bool {
	//: either spelling of the method.
	return receives(t, jsonMarshalerType, addressable) || hasStreamMethod(t, marshalJSONTo, addressable)
}

// writesText reports whether a value of t writes itself as text, which
// encoding/json puts in a JSON string — encoding.TextMarshaler or
// TextAppender — where it is encoded, addressable or not.
func writesText(t reflect.Type, addressable bool) bool {
	//: either spelling of the method.
	return receives(t, textMarshalerType, addressable) || receives(t, textAppenderType, addressable)
}

// hasAnyMethod reports whether t has, on either receiver, any method
// encoding/json would call to encode or decode it — the TYPE-level test
// json/v2 applies to a byte slice's element and to an unexported embedded
// struct, whatever the value's addressability.
func hasAnyMethod(t reflect.Type) bool {
	//: the marshalers, then the unmarshalers.
	return writesJSON(t, true) || writesText(t, true) ||
		receives(t, jsonUnmarshalerType, true) || receives(t, textUnmarshalerType, true) ||
		hasStreamMethod(t, unmarshalJSONFrom, true)
}

// isJSONTextValue reports whether t is jsontext.Value, the other type json/v2
// lets an embedded field collect unknown members into.
func isJSONTextValue(t reflect.Type) bool {
	//: matched by name, as the streaming methods are.
	return t.PkgPath() == jsontextPackage && t.Name() == "Value"
}
