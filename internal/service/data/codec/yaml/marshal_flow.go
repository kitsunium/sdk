// Package yaml — encoding: collections in flow style, for the flow tag.
package yaml

import (
	"maps"
	"reflect"
	"slices"
	"strconv"
	"unicode/utf8"
)

// openFlow writes what precedes a flow collection placed at.
func (e *encoder) openFlow(at place) {
	//: a space after the key's ":" or the entry's "-".
	e.separate(at)
}

// finishFlow ends the line of a flow collection that was written without
// error.
func (e *encoder) finishFlow(err error) error {
	//: a refusal inside the collection.
	if err != nil {
		//: refused.
		return err
	}
	e.buf.WriteByte('\n')
	//: written.
	return nil
}

// writeFlowAny writes x in flow style, with no line break: collections in
// brackets, strings plain when flow-safe and double-quoted otherwise.
func (e *encoder) writeFlowAny(x any) error {
	switch x := x.(type) {
	//: the untyped mapping.
	case map[string]any:
		//: its entries, keys sorted.
		return e.writeFlowStringMap(x, slices.Sorted(maps.Keys(x)))
	//: the untyped sequence.
	case []any:
		//: its entries.
		return e.writeFlowList(x)
	//: a string.
	case string:
		//: plain or quoted.
		return e.writeFlowString(x)
	}
	//: an untyped scalar.
	if e.appendUntypedScalar(x) {
		//: written.
		return nil
	}
	//: anything else, by reflection.
	return e.writeFlowValue(reflect.ValueOf(x))
}

// appendUntypedScalar writes nil, a bool, an int or a float64 in flow style,
// and reports whether x was one.
func (e *encoder) appendUntypedScalar(x any) bool {
	switch x := x.(type) {
	//: null.
	case nil:
		e.buf.WriteString("null")
	//: a boolean.
	case bool:
		e.buf.WriteString(strconv.FormatBool(x))
	//: an int.
	case int:
		e.buf.Write(strconv.AppendInt(e.buf.AvailableBuffer(), int64(x), decimalBase))
	//: a float64.
	case float64:
		e.buf.Write(appendFloat(e.buf.AvailableBuffer(), x, float64Bits))
	//: anything else.
	default:
		//: not written here.
		return false
	}
	//: written.
	return true
}

// writeFlowString writes s in a flow collection.
func (e *encoder) writeFlowString(s string) error {
	//: YAML is Unicode text.
	if !validText(s) {
		//: refused.
		return marshalError("a string is not valid UTF-8", "")
	}
	//: plain.
	if plainSafe(s, true) {
		e.buf.WriteString(s)
		//: written.
		return nil
	}
	appendDoubleQuoted(e.buf, s)
	//: written.
	return nil
}

// writeFlowValue writes the reflected value v in flow style.
func (e *encoder) writeFlowValue(v reflect.Value) error {
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
		e.buf.WriteString("null")
		//: written.
		return nil
	//: a hook's text.
	case shapeText:
		//: as a string.
		return e.writeFlowString(r.text)
	//: a hook's replacement.
	case shapeAny:
		//: in v's place.
		return e.writeFlowAny(r.replacement)
	//: a value.
	default:
		//: by kind.
		return e.writeFlowKind(r.value)
	}
}

// writeFlowKind writes v, neither a pointer nor an interface, in flow style.
func (e *encoder) writeFlowKind(v reflect.Value) error {
	switch v.Kind() {
	//: a map.
	case reflect.Map:
		//: its entries, keys sorted.
		return e.writeFlowMapValue(v)
	//: a struct.
	case reflect.Struct:
		//: its fields.
		return e.writeFlowStructValue(v)
	//: a sequence.
	case reflect.Slice, reflect.Array:
		//: its entries.
		return e.writeFlowSequence(v)
	//: a string.
	case reflect.String:
		//: plain or quoted.
		return e.writeFlowString(v.String())
	//: a boolean or a number.
	default:
		//: a kind with no representation.
		if !e.appendScalarKind(v) {
			//: refused, naming the type.
			return marshalError("the value's kind has no YAML representation", v.Type().String())
		}
		//: written.
		return nil
	}
}

// writeFlowMapValue writes a reflected map in flow style.
func (e *encoder) writeFlowMapValue(v reflect.Value) error {
	//: the untyped one without reflection.
	if m, ok := reflect.TypeAssert[map[string]any](v); ok {
		//: as writeFlowAny writes it.
		return e.writeFlowStringMap(m, slices.Sorted(maps.Keys(m)))
	}
	keys := v.MapKeys()
	slices.SortFunc(keys, compareKeys)
	//: its entries.
	return e.writeFlowMap(v, keys)
}

// writeFlowStructValue writes a struct in flow style.
func (e *encoder) writeFlowStructValue(v reflect.Value) error {
	info := structInfoOf(v.Type())
	//: a malformed tag.
	if info.tagError != "" {
		//: refused.
		return marshalError(info.tagError, v.Type().String())
	}
	inline, err := inlineKeys(v, info)
	//: an inline key colliding with a field.
	if err != nil {
		//: refused.
		return err
	}
	//: its fields.
	return e.writeFlowStruct(v, info, inline)
}

// writeFlowStringMap writes a map[string]any in flow style, keys in order.
func (e *encoder) writeFlowStringMap(m map[string]any, keys []string) error {
	e.buf.WriteByte('{')
	//: every entry.
	for i, k := range keys {
		e.flowSeparator(i)
		//: the key.
		if err := e.writeFlowKey(k); err != nil {
			//: refused.
			return err
		}
		//: the value.
		if err := e.writeFlowAny(m[k]); err != nil {
			//: refused.
			return err
		}
	}
	e.buf.WriteByte('}')
	//: written.
	return nil
}

// flowSeparator writes ", " before every entry but the first.
func (e *encoder) flowSeparator(i int) {
	//: not the first.
	if i > 0 {
		e.buf.WriteString(", ")
	}
}

// writeFlowList writes a []any in flow style.
func (e *encoder) writeFlowList(list []any) error {
	e.buf.WriteByte('[')
	//: every entry.
	for i, item := range list {
		e.flowSeparator(i)
		//: the entry.
		if err := e.writeFlowAny(item); err != nil {
			//: refused.
			return err
		}
	}
	e.buf.WriteByte(']')
	//: written.
	return nil
}

// writeFlowMap writes a map in flow style, keys in the order given.
func (e *encoder) writeFlowMap(v reflect.Value, keys []reflect.Value) error {
	e.buf.WriteByte('{')
	//: every entry.
	for i, k := range keys {
		e.flowSeparator(i)
		//: the key and the value.
		if err := e.writeFlowEntry(k, v.MapIndex(k)); err != nil {
			//: refused.
			return err
		}
	}
	e.buf.WriteByte('}')
	//: written.
	return nil
}

// writeFlowEntry writes a reflected key, its ": ", and its value, in flow
// style.
func (e *encoder) writeFlowEntry(k, value reflect.Value) error {
	text, err := keyText(k, 0)
	//: not a scalar key.
	if err != nil {
		//: refused.
		return err
	}
	//: a string key, quoted as needed.
	if text.isString {
		//: the key.
		if err := e.writeFlowKey(text.text); err != nil {
			//: refused.
			return err
		}
	} else {
		e.buf.WriteString(text.text)
		e.buf.WriteString(": ")
	}
	//: the value.
	return e.writeFlowValue(value)
}

// writeFlowStruct writes a struct in flow style: its visible fields, then its
// inline map's entries.
func (e *encoder) writeFlowStruct(v reflect.Value, info *structInfo, inline []reflect.Value) error {
	e.buf.WriteByte('{')
	written, err := e.writeFlowFields(v, info)
	//: a field failed.
	if err != nil {
		//: refused.
		return err
	}
	m, _ := fieldForRead(v, info.inlineMap)
	//: the inline map's entries.
	for i, k := range inline {
		e.flowSeparator(written + i)
		//: the key and the value.
		if err := e.writeFlowEntry(k, m.MapIndex(k)); err != nil {
			//: refused.
			return err
		}
	}
	e.buf.WriteByte('}')
	//: written.
	return nil
}

// writeFlowFields writes v's visible fields in flow style and returns how
// many it wrote.
func (e *encoder) writeFlowFields(v reflect.Value, info *structInfo) (int, error) {
	written := 0
	//: every field.
	for _, f := range info.fields {
		fv, ok := fieldForRead(v, f.index)
		//: absent, or empty and omitempty.
		if !ok || (f.omitEmpty && isEmpty(fv)) {
			continue
		}
		e.flowSeparator(written)
		written++
		//: the key.
		if err := e.writeFlowKey(f.key); err != nil {
			//: refused.
			return written, err
		}
		//: the value.
		if err := e.writeFlowValue(fv); err != nil {
			//: refused.
			return written, err
		}
	}
	//: the count.
	return written, nil
}

// writeFlowSequence writes a slice or an array in flow style.
func (e *encoder) writeFlowSequence(v reflect.Value) error {
	e.buf.WriteByte('[')
	//: every entry.
	for i := range v.Len() {
		e.flowSeparator(i)
		//: the entry.
		if err := e.writeFlowValue(v.Index(i)); err != nil {
			//: refused.
			return err
		}
	}
	e.buf.WriteByte(']')
	//: written.
	return nil
}

// writeFlowKey writes a string key and its ": " in a flow mapping.
func (e *encoder) writeFlowKey(key string) error {
	start := e.buf.Len()
	//: the key's text.
	if err := e.appendStringKey(key, true); err != nil {
		//: refused.
		return err
	}
	//: YAML's bound on an implicit key, in characters as the decoder counts.
	if utf8.RuneCount(e.buf.Bytes()[start:]) > maxKeyRunes {
		//: refused.
		return marshalError("a mapping key longer than 1024 characters cannot be written as an implicit key", "")
	}
	e.buf.WriteString(": ")
	//: written.
	return nil
}
