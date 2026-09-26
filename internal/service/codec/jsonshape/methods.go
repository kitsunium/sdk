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

// implements reports whether t or *t implements iface: encoding/json calls a
// pointer receiver's method whenever the value is addressable.
func implements(t, iface reflect.Type) bool {
	//: a value receiver, then a pointer receiver.
	return t.Implements(iface) || reflect.PointerTo(t).Implements(iface)
}

// hasStreamMethod reports whether t or *t has json/v2's streaming method of
// that name: func(*jsontext.<param>) error. It is matched by signature so this
// package does not import json/v2 to recognise it.
func hasStreamMethod(t reflect.Type, name, param string) bool {
	//: a value receiver, then a pointer receiver.
	return streamMethodOn(t, name, param) || streamMethodOn(reflect.PointerTo(t), name, param)
}

// streamMethodOn reports whether the method set of receiver holds the
// streaming method of that name and signature.
func streamMethodOn(receiver reflect.Type, name, param string) bool {
	method, found := receiver.MethodByName(name)
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

// writesJSON reports whether t writes its own JSON: json.Marshaler, or
// json/v2's MarshalerTo.
func writesJSON(t reflect.Type) bool {
	//: either spelling of the method.
	return implements(t, jsonMarshalerType) || hasStreamMethod(t, "MarshalJSONTo", "Encoder")
}

// writesText reports whether t writes itself as text, which encoding/json puts
// in a JSON string: encoding.TextMarshaler or encoding.TextAppender.
func writesText(t reflect.Type) bool {
	//: either spelling of the method.
	return implements(t, textMarshalerType) || implements(t, textAppenderType)
}

// hasAnyMethod reports whether t has any method encoding/json would call to
// encode or decode it — the test json/v2 applies before letting an unexported
// embedded struct stand as a member.
func hasAnyMethod(t reflect.Type) bool {
	//: the marshalers, then the unmarshalers.
	return writesJSON(t) || writesText(t) ||
		implements(t, jsonUnmarshalerType) || implements(t, textUnmarshalerType) ||
		hasStreamMethod(t, "UnmarshalJSONFrom", "Decoder")
}

// isJSONTextValue reports whether t is jsontext.Value, the other type json/v2
// lets an embedded field collect unknown members into.
func isJSONTextValue(t reflect.Type) bool {
	//: matched by name, as the streaming methods are.
	return t.PkgPath() == jsontextPackage && t.Name() == "Value"
}
