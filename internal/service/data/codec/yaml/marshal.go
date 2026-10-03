// Package yaml — encoding: Go values resolved, then written in block style.
package yaml

import (
	"bytes"
	"encoding"
	"reflect"
	"strconv"
	"time"

	coreyaml "github.com/kitsunium/sdk/internal/core/data/codec/yaml"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// indentStep is how many spaces each nesting level indents by.
const indentStep int = 2

// rootColumn is the column of the collection "holding" the root, so that
// the root's own entries sit at column 0.
const rootColumn int = -indentStep

// The three places a value starts at.
const (
	// atRoot is the start of the document.
	atRoot place = iota
	// afterKey is just after a mapping key's ":".
	afterKey
	// afterDash is just after a sequence entry's "-".
	afterDash
)

// The shapes a value takes once its pointers, interfaces and hooks are
// resolved.
const (
	// shapeNull writes null.
	shapeNull shape = iota
	// shapeText writes a string: a MarshalText result or a duration's text.
	shapeText
	// shapeAny writes the value a MarshalYAML hook returned.
	shapeAny
	// shapeValue writes a reflected value by its kind.
	shapeValue
)

// place is where a value is written.
type place uint8

// shape is what a resolved value writes as.
type shape uint8

// encoder writes one document into buf.
type encoder struct {
	// buf receives the document.
	buf *bytes.Buffer
	// depth is how many values enclose the one being written.
	depth int
}

// resolved is a value once its pointers, interfaces and hooks are resolved.
type resolved struct {
	// replacement is what a MarshalYAML hook returned, for shapeAny.
	replacement any
	// value is the value to write by kind, for shapeValue.
	value reflect.Value
	// text is the string to write, for shapeText.
	text string
	// shape says which of the three is written.
	shape shape
}

// encodeDocument writes v as one document.
func (e *encoder) encodeDocument(v any) error {
	//: the root.
	return e.writeAny(v, rootColumn, atRoot, false)
}

// enter counts one level of nesting, refusing a value nested past maxDepth —
// which is how a cyclic value is refused instead of recursing forever.
func (e *encoder) enter() error {
	e.depth++
	//: the bound.
	if e.depth > maxDepth {
		//: refused.
		return marshalError("the value nests deeper than the encoder accepts", "")
	}
	//: within it.
	return nil
}

// leave undoes enter.
func (e *encoder) leave() {
	e.depth--
}

// writeAny writes the value x, placed at, inside a collection whose entries
// sit at column col. flow writes a collection in flow style. The common
// untyped shapes are written without reflection.
func (e *encoder) writeAny(x any, col int, at place, flow bool) error {
	switch x := x.(type) {
	//: the untyped mapping.
	case map[string]any:
		//: its entries, keys sorted.
		return e.writeStringMap(x, col, at, flow)
	//: the untyped sequence.
	case []any:
		//: its entries.
		return e.writeList(x, col, at, flow)
	//: a string.
	case string:
		//: plain, quoted or literal.
		return e.writeString(x, col, at)
	}
	//: an untyped scalar.
	if e.writeUntypedScalar(x, at) {
		//: written.
		return nil
	}
	//: anything else, by reflection.
	return e.writeValue(reflect.ValueOf(x), col, at, flow)
}

// writeUntypedScalar writes nil, a bool, an int or a float64 without
// reflection, and reports whether x was one.
func (e *encoder) writeUntypedScalar(x any, at place) bool {
	switch x := x.(type) {
	//: null.
	case nil:
		e.writeScalarText("null", at)
	//: a boolean.
	case bool:
		e.writeScalarText(strconv.FormatBool(x), at)
	//: an int.
	case int:
		e.writeInt(int64(x), at)
	//: a float64.
	case float64:
		e.writeFloat(x, float64Bits, at)
	//: anything else.
	default:
		//: not written here.
		return false
	}
	//: written.
	return true
}

// writeValue writes the reflected value v; see writeAny.
func (e *encoder) writeValue(v reflect.Value, col int, at place, flow bool) error {
	//: one level deeper.
	if err := e.enter(); err != nil {
		//: refused.
		return err
	}
	defer e.leave()
	r, err := e.resolve(v)
	//: a hook failed, or a chain of pointers is too long.
	if err != nil {
		//: refused.
		return err
	}
	switch r.shape {
	//: nil.
	case shapeNull:
		e.writeScalarText("null", at)
		//: written.
		return nil
	//: a hook's text.
	case shapeText:
		//: as a string.
		return e.writeString(r.text, col, at)
	//: a hook's replacement.
	case shapeAny:
		//: in v's place.
		return e.writeAny(r.replacement, col, at, flow)
	//: a value.
	default:
		//: by kind.
		return e.writeKind(r.value, col, at, flow)
	}
}

// resolve walks v's pointers and interfaces and applies the first hook met:
// MarshalYAML, MarshalText, a duration's text. A chain of indirections counts
// against the depth bound, so a cycle of pointers ends.
func (e *encoder) resolve(v reflect.Value) (resolved, error) {
	//: one indirection per step.
	for steps := 0; ; steps++ {
		//: the bound.
		if e.depth+steps > maxDepth {
			//: refused.
			return resolved{}, marshalError("the value nests deeper than the encoder accepts", "")
		}
		//: nil.
		if isNilValue(v) {
			//: null.
			return resolved{shape: shapeNull}, nil
		}
		//: a hook decides what is written.
		if r, hooked, err := applyHooks(v); hooked || err != nil {
			//: the hook's result.
			return r, err
		}
		//: neither a pointer nor an interface: the value itself.
		if v.Kind() != reflect.Pointer && v.Kind() != reflect.Interface {
			//: by kind.
			return resolved{shape: shapeValue, value: v}, nil
		}
		v = v.Elem()
	}
}

// isNilValue reports whether v writes as null: invalid, or a nil pointer or
// interface. A nil map or slice writes as an empty collection, as in yaml.v3.
func isNilValue(v reflect.Value) bool {
	//: no value.
	if !v.IsValid() {
		//: null.
		return true
	}
	k := v.Kind()
	//: a nil pointer or interface.
	return (k == reflect.Pointer || k == reflect.Interface) && v.IsNil()
}

// applyHooks returns what a hook of v's type writes in v's place, and whether
// it has one: MarshalYAML first, then MarshalText, then a duration's text. A
// pointer-receiver hook applies where v is addressable.
func applyHooks(v reflect.Value) (resolved, bool, error) {
	t := v.Type()
	//: a predeclared type has no method.
	if !mayHaveHooks(t) {
		//: no hook.
		return resolved{}, false, nil
	}
	h := hooksOf(t)
	switch {
	//: MarshalYAML.
	case usesHook(h, hookMarshaler, hookPtrMarshaler, v):
		//: its replacement.
		return callMarshalYAML(receiver(v, h.has(hookPtrMarshaler)), t)
	//: MarshalText.
	case usesHook(h, hookTextMarshaler, hookPtrTextMarshaler, v):
		//: its text.
		return callMarshalText(receiver(v, h.has(hookPtrTextMarshaler)), t)
	//: a duration, as its text.
	case t == durationType:
		//: "1h30m0s".
		return resolved{shape: shapeText, text: time.Duration(v.Int()).String()}, true, nil
	}
	//: no hook.
	return resolved{}, false, nil
}

// usesHook reports whether v is written through the hook own, or through
// viaPointer when v is addressable.
func usesHook(h, own, viaPointer typeHooks, v reflect.Value) bool {
	//: the value's own method, or its pointer's.
	return h.has(own) || (h.has(viaPointer) && v.CanAddr())
}

// receiver returns the value a hook is called on: v, or its address for a
// pointer-receiver hook.
func receiver(v reflect.Value, viaPointer bool) reflect.Value {
	//: the pointer receiver.
	if viaPointer {
		//: v's address.
		return v.Addr()
	}
	//: the value receiver.
	return v
}

// callMarshalYAML calls MarshalYAML on target, a value of type t or a pointer
// to one.
func callMarshalYAML(target reflect.Value, t reflect.Type) (resolved, bool, error) {
	m, ok := marshalYAMLHook(target)
	//: hooksOf said it implements the hook.
	if !ok {
		//: no hook after all.
		return resolved{}, false, nil
	}
	replacement, err := m.MarshalYAML()
	//: the hook failed.
	if err != nil {
		//: wrapped, its own code kept when it has one.
		return resolved{}, true, hookMarshalFailed(err, t)
	}
	//: the replacement.
	return resolved{shape: shapeAny, replacement: replacement}, true, nil
}

// callMarshalText calls MarshalText on target, a value of type t or a pointer
// to one.
func callMarshalText(target reflect.Value, t reflect.Type) (resolved, bool, error) {
	m, ok := reflect.TypeAssert[encoding.TextMarshaler](target)
	//: hooksOf said it implements the hook.
	if !ok {
		//: no hook after all.
		return resolved{}, false, nil
	}
	text, err := m.MarshalText()
	//: the hook failed.
	if err != nil {
		//: wrapped, its own code kept when it has one.
		return resolved{}, true, hookMarshalFailed(err, t)
	}
	//: the text.
	return resolved{shape: shapeText, text: string(text)}, true, nil
}

// hookMarshalFailed wraps the error a type's own MarshalYAML or MarshalText
// returned. A cause that is already an SDK error keeps its code; any other
// becomes MarshalFailed.
func hookMarshalFailed(cause error, t reflect.Type) error {
	//: the hook's error, with the type that raised it.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    coreyaml.CodeYAMLMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "YAML encoding failed",
		Private: "service/data/codec/yaml: a type's own encoding hook returned an error",
	}, errs.String("type", t.String()))
}

// writeKind writes v, which is neither a pointer nor an interface, by kind.
func (e *encoder) writeKind(v reflect.Value, col int, at place, flow bool) error {
	switch v.Kind() {
	//: a map.
	case reflect.Map:
		//: its entries, keys sorted.
		return e.writeMap(v, col, at, flow)
	//: a struct.
	case reflect.Struct:
		//: its fields, in order.
		return e.writeStruct(v, col, at, flow)
	//: a sequence: bytes in flow style, short to write and long to read in
	//: block style; integers, as yaml.v3 wrote them.
	case reflect.Slice, reflect.Array:
		//: its entries.
		return e.writeSequence(v, col, at, flow || v.Type().Elem().Kind() == reflect.Uint8)
	//: a string.
	case reflect.String:
		//: plain, quoted or literal.
		return e.writeString(v.String(), col, at)
	//: a boolean or a number.
	default:
		e.separate(at)
		//: a channel, a function, a complex number, an unsafe pointer.
		if !e.appendScalarKind(v) {
			//: refused, naming the type.
			return marshalError("the value's kind has no YAML representation", v.Type().String())
		}
		e.buf.WriteByte('\n')
		//: written.
		return nil
	}
}

// appendScalarKind writes a boolean or a number's text, and reports whether v
// was one.
func (e *encoder) appendScalarKind(v reflect.Value) bool {
	switch v.Kind() {
	//: a boolean.
	case reflect.Bool:
		e.buf.WriteString(strconv.FormatBool(v.Bool()))
	//: a signed integer.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		e.buf.Write(strconv.AppendInt(e.buf.AvailableBuffer(), v.Int(), decimalBase))
	//: an unsigned integer.
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		e.buf.Write(strconv.AppendUint(e.buf.AvailableBuffer(), v.Uint(), decimalBase))
	//: a float.
	case reflect.Float32, reflect.Float64:
		e.buf.Write(appendFloat(e.buf.AvailableBuffer(), v.Float(), v.Type().Bits()))
	//: anything else.
	default:
		//: not a scalar.
		return false
	}
	//: written.
	return true
}

// separate writes the space after a key's ":" or an entry's "-".
func (e *encoder) separate(at place) {
	//: nothing at the root.
	if at != atRoot {
		e.buf.WriteByte(' ')
	}
}

// writeScalarText writes a scalar's text, placed at.
func (e *encoder) writeScalarText(text string, at place) {
	e.separate(at)
	e.buf.WriteString(text)
	e.buf.WriteByte('\n')
}

// writeInt writes an integer, placed at. The digits are formatted into the
// buffer's spare capacity after the separator is written, so nothing is
// allocated and nothing overwrites them.
func (e *encoder) writeInt(i int64, at place) {
	e.separate(at)
	e.buf.Write(strconv.AppendInt(e.buf.AvailableBuffer(), i, decimalBase))
	e.buf.WriteByte('\n')
}

// writeFloat writes a float of bits bits, placed at, as writeInt does.
func (e *encoder) writeFloat(f float64, bits int, at place) {
	e.separate(at)
	e.buf.Write(appendFloat(e.buf.AvailableBuffer(), f, bits))
	e.buf.WriteByte('\n')
}
