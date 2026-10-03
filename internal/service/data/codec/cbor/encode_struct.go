// Package cbor — structs on the encoding side: a map keyed by field name (or
// by integer, keyasint), or an array (toarray); omitempty and omitzero.
package cbor

import "reflect"

// maxHeadBytes is the longest head: an initial byte and eight argument bytes.
const maxHeadBytes int = 9

// structEncoder writes a struct as a map of its fields.
type structEncoder struct{}

// structArrayEncoder writes a toarray struct as an array of its fields.
type structArrayEncoder struct{}

// fillStruct plans a struct from its wire layout. A field of a type CBOR
// cannot carry refuses the whole struct, as fxamacker/cbor did.
func (pl *encodePlanner) fillStruct(p *encodePlan) {
	layout := layoutOf(p.typ)
	//: an unparsable keyasint name refuses the type.
	if layout.refusal != "" {
		refuse(p, layout.refusal+" in "+p.typ.String())
		//: refused.
		return
	}
	p.fields = make([]encodeField, 0, len(layout.fields))
	//: each field's key and plan.
	for _, f := range layout.fields {
		field, refusal := pl.encodeFieldOf(f)
		//: one field that cannot be written refuses the struct.
		if refusal != "" {
			refuse(p, refusal)
			//: refused.
			return
		}
		p.fields = append(p.fields, field)
		//: counted for the struct's own omitempty answer.
		if field.flags&flagOmitEmpty != 0 {
			p.omittable++
		}
	}
	//: toarray ignores omitempty and omitzero: every element is written.
	if layout.toArray {
		p.kind, p.zero = structArrayEncoder{}, zeroNoFields
		//: an array.
		return
	}
	p.kind = structEncoder{}
}

// encodeFieldOf resolves one field: its encoded key and its plan.
func (pl *encodePlanner) encodeFieldOf(f *structField) (field encodeField, refusal string) {
	plan := pl.plan(f.typ)
	//: the field's type cannot be carried.
	if plan.refusal != "" {
		//: the type's refusal.
		return encodeField{}, plan.refusal
	}
	key, err := fieldKey(f)
	//: a tag can spell a name that is not UTF-8.
	if err != nil {
		//: refused.
		return encodeField{}, "a field name that is not valid UTF-8"
	}
	//: ready to write.
	return encodeField{key: key, index: f.index, plan: plan, flags: f.flags}, ""
}

// fieldKey encodes a field's key once, when the plan is built.
func fieldKey(f *structField) ([]byte, error) {
	//: an integer key for keyasint.
	if f.has(flagKeyAsInt) {
		//: major type 0 or 1.
		return appendInt(nil, f.nameInt), nil
	}
	//: a text string otherwise.
	return appendText(nil, f.name)
}

// encode appends the struct as a map. The head is written for every field;
// when omitempty, omitzero or a nil embedded pointer leaves some out, it is
// rewritten — shorter when the count crosses a head width.
func (structEncoder) encode(b []byte, v reflect.Value, p *encodePlan, at walkDepth) ([]byte, error) {
	inner, err := at.enter()
	//: a map is one level of nesting.
	if err != nil {
		//: too deep.
		return b, err
	}
	start := len(b)
	b = appendHead(b, majorMap, uint64(len(p.fields)))
	headLen := len(b) - start
	written := 0
	//: each field, in declaration order.
	for i := range p.fields {
		var wrote bool
		b, wrote, err = appendField(b, v, &p.fields[i], inner)
		//: the first failure ends the encoding.
		if err != nil {
			//: refused.
			return b, err
		}
		//: counted for the head.
		if wrote {
			written++
		}
	}
	//: the head says how many pairs were written.
	return rewriteMapHead(b, start, headLen, written, len(p.fields)), nil
}

// empty is true when every field carries omitempty and every field is
// empty — the encoding would be an empty map.
func (structEncoder) empty(v reflect.Value, p *encodePlan) (bool, error) {
	//: a field without omitempty is always written.
	if p.omittable < len(p.fields) {
		//: not empty.
		return false, nil
	}
	//: each field in turn.
	for i := range p.fields {
		f := &p.fields[i]
		fv, ok := fieldValue(v, f.index)
		//: absent behind a nil embedded pointer.
		if !ok {
			continue
		}
		empty, err := f.plan.kind.empty(fv, f.plan)
		//: one written field makes the struct not empty.
		if err != nil || !empty {
			//: not empty, or refused.
			return false, err
		}
	}
	//: an empty map.
	return true, nil
}

// appendField appends one key/value pair, unless the field is omitted or
// promoted through a nil embedded pointer.
func appendField(b []byte, v reflect.Value, f *encodeField, at walkDepth) (encoded []byte, wrote bool, err error) {
	fv, ok := fieldValue(v, f.index)
	//: an embedded pointer on the way is nil.
	if !ok {
		//: nothing written.
		return b, false, nil
	}
	omit, err := f.omitted(fv)
	//: omitted, or the question failed (MarshalBinary).
	if err != nil || omit {
		//: nothing written.
		return b, false, err
	}
	b = append(b, f.key...)
	b, err = f.plan.kind.encode(b, fv, f.plan, at)
	//: one pair, unless refused.
	return b, err == nil, err
}

// rewriteMapHead makes the map head at b[start:] count written pairs instead
// of declared ones, moving the pairs left when the head shrinks.
func rewriteMapHead(b []byte, start, headLen, written, declared int) []byte {
	//: every field was written: the head is already right.
	if written == declared {
		//: unchanged.
		return b
	}
	var scratch [maxHeadBytes]byte
	head := appendHead(scratch[:0], majorMap, uint64(written))
	//: a smaller count can need a narrower head.
	if shrink := headLen - len(head); shrink > 0 {
		copy(b[start+len(head):], b[start+headLen:])
		b = b[:len(b)-shrink]
	}
	copy(b[start:], head)
	//: the corrected map.
	return b
}

// encode appends a toarray struct: one element per field, in declaration
// order, null for one promoted through a nil embedded pointer.
func (structArrayEncoder) encode(b []byte, v reflect.Value, p *encodePlan, at walkDepth) ([]byte, error) {
	inner, err := at.enter()
	//: an array is one level of nesting.
	if err != nil {
		//: too deep.
		return b, err
	}
	b = appendHead(b, majorArray, uint64(len(p.fields)))
	//: every field, always.
	for i := range p.fields {
		f := &p.fields[i]
		fv, ok := fieldValue(v, f.index)
		//: an embedded pointer on the way is nil.
		if !ok {
			b = append(b, nullByte)
			continue
		}
		b, err = f.plan.kind.encode(b, fv, f.plan, inner)
		//: the first failure ends the encoding.
		if err != nil {
			//: refused.
			return b, err
		}
	}
	//: the whole array.
	return b, nil
}

// empty is true for a toarray struct with no field: an empty array.
func (structArrayEncoder) empty(_ reflect.Value, p *encodePlan) (bool, error) {
	//: the layout, not the value, decides.
	return len(p.fields) == 0, nil
}

// fieldValue follows index from the struct v to a field, through embedded
// structs and pointers to them. ok is false when one of those pointers is nil.
func fieldValue(v reflect.Value, index []int) (field reflect.Value, ok bool) {
	field = v.Field(index[0])
	//: one embedded level per remaining step.
	for _, step := range index[1:] {
		//: an embedded *T is followed when set.
		if field.Kind() == reflect.Pointer {
			//: a nil embedded pointer holds no field.
			if field.IsNil() {
				//: absent.
				return field, false
			}
			field = field.Elem()
		}
		field = field.Field(step)
	}
	//: the field.
	return field, true
}

// omitted reports whether the field's options leave v out of the encoding.
func (f *encodeField) omitted(v reflect.Value) (bool, error) {
	//: omitempty asks whether the value encodes as an empty item.
	if f.flags&flagOmitEmpty != 0 {
		empty, err := f.plan.kind.empty(v, f.plan)
		//: empty, or the question failed.
		if err != nil || empty {
			//: omitted, or refused.
			return empty, err
		}
	}
	//: omitzero asks whether the value is its type's zero value.
	if f.flags&flagOmitZero != 0 {
		//: the type's own answer.
		return f.plan.zero(v)
	}
	//: written.
	return false, nil
}

// zeroNoFields answers omitzero for a toarray struct the way fxamacker/cbor
// did: zero only when the struct has no field on the wire.
func zeroNoFields(v reflect.Value) (bool, error) {
	//: the layout, not the value, decides.
	return len(encodePlanFor(v.Type()).fields) == 0, nil
}

// zeroFuncFor picks the omitzero question for t: the type's own IsZero when
// it declares one, reflect's zero value test otherwise — encoding/json's
// rule since Go 1.24.
func zeroFuncFor(t reflect.Type) zeroFunc {
	//: who declares IsZero decides how it is reached.
	switch {
	case t.Kind() == reflect.Interface && t.Implements(isZeroerType):
		//: through the interface, nil being zero.
		return zeroThroughInterface
	case t.Kind() == reflect.Pointer && t.Implements(isZeroerType):
		//: through the pointer, nil being zero.
		return zeroThroughPointer
	case t.Implements(isZeroerType):
		//: on the value.
		return zeroByMethod
	case reflect.PointerTo(t).Implements(isZeroerType):
		//: on the value's address.
		return zeroByAddress
	default:
		//: reflect's zero value test.
		return zeroByValue
	}
}

// zeroThroughInterface is omitzero for an interface type declaring IsZero:
// nil, or holding a nil pointer, is zero; otherwise the method decides.
func zeroThroughInterface(v reflect.Value) (bool, error) {
	//: nothing held.
	if v.IsNil() {
		//: zero.
		return true, nil
	}
	//: a nil pointer cannot be asked.
	if elem := v.Elem(); elem.Kind() == reflect.Pointer && elem.IsNil() {
		//: zero.
		return true, nil
	}
	//: the method's answer.
	return zeroByMethod(v)
}

// zeroThroughPointer is omitzero for a pointer type declaring IsZero: nil is
// zero; otherwise the method decides.
func zeroThroughPointer(v reflect.Value) (bool, error) {
	//: a nil pointer cannot be asked.
	if v.IsNil() {
		//: zero.
		return true, nil
	}
	//: the method's answer.
	return zeroByMethod(v)
}

// zeroByMethod calls the value's own IsZero.
func zeroByMethod(v reflect.Value) (bool, error) {
	z, _ := reflect.TypeAssert[interface{ IsZero() bool }](v)
	//: the type's answer.
	return z.IsZero(), nil
}

// zeroByAddress calls IsZero through the value's address, or through an
// addressable copy when the value has none.
func zeroByAddress(v reflect.Value) (bool, error) {
	z, _ := asMethods[interface{ IsZero() bool }](v)
	//: the type's answer.
	return z.IsZero(), nil
}

// zeroByValue is reflect's zero value test.
func zeroByValue(v reflect.Value) (bool, error) {
	//: every field, element or bit at its zero value.
	return v.IsZero(), nil
}
