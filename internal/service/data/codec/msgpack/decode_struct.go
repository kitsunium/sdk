// Package msgpack — the struct decoder. A struct reads a map by key, through
// the layout struct.go builds, or an array by position — whatever its own
// options say, as the vendor-backed codec read it — and nil zeroes it. An
// inlined field behind a nil embedded pointer allocates that pointer, as
// encoding/json does; one behind a nil pointer to an UNEXPORTED struct cannot
// be allocated and is refused. A field reached through an unexported embedded
// struct that was not inlined decodes when it is a struct itself, and is
// stepped over otherwise, because reflection may not set it.
package msgpack

import (
	"reflect"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fieldDecoder is one struct key with its decoder.
type fieldDecoder struct {
	// field is the layout entry.
	field *structField
	// dec decodes the field's value.
	dec decodeFunc
}

// structDecoder decodes a struct from a map or an array.
type structDecoder struct {
	// byName finds the field a key addresses, aliases included.
	byName map[string]*fieldDecoder
	// fields are the struct's keys in encode order, for the array form.
	fields []*fieldDecoder
}

// newStructDecoder builds the decoder of struct type t from its layout.
func newStructDecoder(t reflect.Type) decodeFunc {
	layout := layoutOf(t)
	//: a type with two fields on one key is refused on every decode.
	if err := layout.defect(t, false); err != nil {
		//: decided once.
		return failingDecoder(err)
	}
	s := &structDecoder{
		byName: make(map[string]*fieldDecoder, len(layout.byName)),
		fields: make([]*fieldDecoder, 0, len(layout.fields)),
	}
	built := make(map[*structField]*fieldDecoder, len(layout.byName))
	//: the encode order, for the array form.
	for _, f := range layout.fields {
		s.fields = append(s.fields, fieldDecoderOf(f, built))
	}
	//: every key, aliases and embedded names included.
	for name, f := range layout.byName {
		s.byName[name] = fieldDecoderOf(f, built)
	}
	//: the plan.
	return s.decode
}

// fieldDecoderOf returns the one fieldDecoder of f, building it once.
func fieldDecoderOf(f *structField, built map[*structField]*fieldDecoder) *fieldDecoder {
	//: an alias shares its field's decoder.
	if fd, ok := built[f]; ok {
		//: already built.
		return fd
	}
	fd := &fieldDecoder{field: f, dec: decoderFor(f.typ)}
	built[f] = fd
	//: built now.
	return fd
}

// decode decodes a map, an array or nil into the struct v.
func (s *structDecoder) decode(d *decodeState, v reflect.Value) error {
	h, err := d.readHeader()
	if err != nil {
		//: malformed or truncated.
		return err
	}
	//: the two forms, or nil.
	switch h.fam {
	//: by key.
	case famMap:
		return s.decodeMap(d, v, h)
	//: by position.
	case famArray:
		return s.decodeArray(d, v, h)
	//: nil is the zero struct.
	case famNil:
		return s.zero(v)
	//: anything else.
	default:
		return d.mismatch(h, v.Type())
	}
}

// decodeMap decodes the map form: each key finds its field, unknown keys are
// stepped over.
func (s *structDecoder) decodeMap(d *decodeState, v reflect.Value, h header) error {
	//: a count the input cannot hold is refused first.
	if err := d.checkCount(h); err != nil {
		//: implausible count.
		return err
	}
	//: one level deeper.
	if err := d.enter(); err != nil {
		//: nested too deep.
		return err
	}
	//: one pair per declared count.
	for range h.arg {
		//: the first failure wins.
		if err := s.decodePair(d, v); err != nil {
			//: stop.
			return err
		}
	}
	d.leave()
	//: decoded.
	return nil
}

// decodePair decodes one key and the value of the field it names.
func (s *structDecoder) decodePair(d *decodeState, v reflect.Value) error {
	key, err := d.readKey()
	if err != nil {
		//: not a string key, or truncated.
		return err
	}
	f := s.byName[string(key)]
	//: a key no field claims is stepped over.
	if f == nil {
		//: consume its value.
		return d.skip()
	}
	//: the field's value.
	return f.decodeInto(d, v)
}

// decodeArray decodes the array form: one element per field, in order. An
// empty array is the zero struct; any other count must match the fields.
func (s *structDecoder) decodeArray(d *decodeState, v reflect.Value, h header) error {
	//: the vendor read an empty array as the zero struct.
	if h.arg == 0 {
		//: zero.
		return s.zero(v)
	}
	//: a positional form must agree on the number of fields.
	if h.arg != uint64(len(s.fields)) {
		//: name both counts.
		return unmarshalFault("array-encoded struct has a different number of fields",
			typeField(v.Type()), errs.Int64(fieldLen, int64(h.arg)), errs.Int(fieldLimit, len(s.fields)))
	}
	//: one level deeper.
	if err := d.enter(); err != nil {
		//: nested too deep.
		return err
	}
	//: one element per field.
	for _, f := range s.fields {
		//: the first failure wins.
		if err := f.decodeInto(d, v); err != nil {
			//: stop.
			return err
		}
	}
	d.leave()
	//: decoded.
	return nil
}

// decodeInto decodes the next value into this field of struct v.
func (f *fieldDecoder) decodeInto(d *decodeState, v reflect.Value) error {
	fv, err := fieldForDecode(v, f.field.index)
	if err != nil {
		//: an embedded pointer that cannot be allocated.
		return err
	}
	//: a non-struct value reached through an unexported embedding cannot be
	//: set: its value is stepped over.
	if !fv.CanSet() && fv.Kind() != reflect.Struct {
		//: consume it.
		return d.skip()
	}
	//: the field's own decoder.
	return f.dec(d, fv)
}

// zero sets the struct v to its zero value — field by field when v itself
// may not be set, which only a struct reached through an unexported
// embedding is.
func (s *structDecoder) zero(v reflect.Value) error {
	//: the usual case.
	if v.CanSet() {
		v.SetZero()
		//: zeroed.
		return nil
	}
	//: the fields reflection may set.
	for _, f := range s.fields {
		fv, ok := fieldValue(v, f.field.index)
		//: settable fields are zeroed, the others left as they are.
		if ok && fv.CanSet() {
			fv.SetZero()
		}
	}
	//: zeroed as far as possible.
	return nil
}

// fieldForDecode follows index from struct v, allocating nil embedded
// pointers on the way.
func fieldForDecode(v reflect.Value, index []int) (reflect.Value, error) {
	//: a field of the struct itself.
	if len(index) == 1 {
		//: direct.
		return v.Field(index[0]), nil
	}
	//: an inlined field: one embedding per step.
	for i, x := range index {
		//: every step after the first may cross a pointer.
		if i > 0 && v.Kind() == reflect.Pointer {
			//: a nil embedding is allocated when it may be.
			if err := allocEmbedded(v); err != nil {
				//: an unexported embedded pointer.
				return reflect.Value{}, err
			}
			v = v.Elem()
		}
		v = v.Field(x)
	}
	//: the field.
	return v, nil
}

// allocEmbedded allocates a nil embedded pointer, refusing one that
// reflection may not set.
func allocEmbedded(v reflect.Value) error {
	//: already allocated.
	if !v.IsNil() {
		//: nothing to do.
		return nil
	}
	//: an unexported embedded pointer cannot be set from outside its package.
	if !v.CanSet() {
		//: refuse, as encoding/json does.
		return unmarshalFault("cannot allocate an embedded pointer to an unexported struct", typeField(v.Type()))
	}
	v.Set(reflect.New(v.Type().Elem()))
	//: allocated.
	return nil
}
