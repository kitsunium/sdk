// Package cbor — decoding into structs. A map's text keys find their field
// exactly, then ignoring case; its integer keys find a keyasint field; a key
// no field has is skipped with its value, and a key seen twice keeps its
// first value — fxamacker/cbor's rules. A toarray struct takes an array of
// exactly as many elements as it has fields.
package cbor

import (
	"bytes"
	"math"
	"reflect"
)

// The thresholds of a struct's field lookup.
const (
	// linearLookupMax is the field count up to which a key is looked up by
	// scanning the fields, which beats hashing it.
	linearLookupMax int = 8
	// smallFieldSet is how many fields a bit set on the stack can track.
	smallFieldSet int = 64
)

// stringType is the Go string type, for the failures of a string target.
var stringType = reflect.TypeFor[string]()

// decodeFields is a struct's fields as the decoder looks them up.
type decodeFields struct {
	// list is the fields on the wire, in declaration order.
	list []decodeField
	// byName indexes the text-keyed fields of a struct with many of them.
	byName map[string]int
	// toArray is the toarray option.
	toArray bool
}

// decodeField is one struct field as the decoder fills it.
type decodeField struct {
	// name is the text key.
	name string
	// nameBytes is the text key as bytes, for comparing with the input.
	nameBytes []byte
	// path names the field in failure messages: "Type.key".
	path string
	// index is the path from the struct to the field.
	index []int
	// plan stores into the field.
	plan *decodePlan
	// nameInt is the key of a keyasint field.
	nameInt int64
	// keyAsInt is true when the field's key is the integer nameInt.
	keyAsInt bool
}

// fieldSet records which fields a map has already set, so a repeated key
// keeps its first value.
type fieldSet struct {
	// small holds the set of a struct with up to 64 fields.
	small uint64
	// large holds the set of a larger struct.
	large []bool
}

// fillStruct plans a struct from its wire layout.
func (pl *decodePlanner) fillStruct(p *decodePlan) {
	layout := layoutOf(p.typ)
	//: an unparsable keyasint name refuses the type.
	if layout.refusal != "" {
		p.refusal, p.kind = layout.refusal+" in "+p.typ.String(), refusedDecoder{}
		//: refused.
		return
	}
	fields := &decodeFields{toArray: layout.toArray, list: make([]decodeField, len(layout.fields))}
	//: each field's key and plan.
	for i, f := range layout.fields {
		fields.list[i] = decodeField{
			name: f.name, nameBytes: []byte(f.name), path: p.typ.String() + "." + f.name, index: f.index,
			plan: pl.plan(f.typ), nameInt: f.nameInt, keyAsInt: f.has(flagKeyAsInt),
		}
	}
	fields.indexNames()
	p.fields, p.kind = fields, structDecoder{}
}

// indexNames builds the name index of a struct with many fields.
func (fs *decodeFields) indexNames() {
	//: a scan is faster for a few fields.
	if len(fs.list) <= linearLookupMax {
		//: no index.
		return
	}
	fs.byName = make(map[string]int, len(fs.list))
	//: text keys only.
	for i := range fs.list {
		//: a keyasint field is looked up by integer.
		if !fs.list[i].keyAsInt {
			fs.byName[fs.list[i].name] = i
		}
	}
}

// byText finds the field a text key names: exactly, then ignoring case.
func (fs *decodeFields) byText(key []byte) (int, bool) {
	//: an exact match wins.
	if i, ok := fs.exact(key); ok {
		//: found.
		return i, true
	}
	//: a key differing only in case, as encoding/json allows.
	return fs.folded(key)
}

// exact finds the field whose key is exactly key.
func (fs *decodeFields) exact(key []byte) (int, bool) {
	//: hashed for a large struct.
	if fs.byName != nil {
		i, ok := fs.byName[string(key)]
		//: no conversion is allocated for a map lookup.
		return i, ok
	}
	//: scanned for a small one.
	for i := range fs.list {
		//: compared without allocating.
		if !fs.list[i].keyAsInt && bytes.Equal(fs.list[i].nameBytes, key) {
			//: found.
			return i, true
		}
	}
	//: none.
	return -1, false
}

// folded finds the first field whose key equals key ignoring case.
func (fs *decodeFields) folded(key []byte) (int, bool) {
	//: in declaration order.
	for i := range fs.list {
		f := &fs.list[i]
		//: same length and same letters, whatever their case.
		if !f.keyAsInt && len(f.nameBytes) == len(key) && bytes.EqualFold(f.nameBytes, key) {
			//: found.
			return i, true
		}
	}
	//: none.
	return -1, false
}

// byInt finds the keyasint field keyed by n.
func (fs *decodeFields) byInt(n int64) (int, bool) {
	//: scanned: integer keys belong to small, protocol-shaped structs.
	for i := range fs.list {
		//: the integer key.
		if fs.list[i].keyAsInt && fs.list[i].nameInt == n {
			//: found.
			return i, true
		}
	}
	//: none.
	return -1, false
}

// newFieldSet returns an empty set for a struct of n fields.
func newFieldSet(n int) fieldSet {
	//: a bit per field on the stack, when they fit.
	if n <= smallFieldSet {
		//: no allocation.
		return fieldSet{}
	}
	//: a flag per field otherwise.
	return fieldSet{large: make([]bool, n)}
}

// has reports whether field i was set.
func (s *fieldSet) has(i int) bool {
	//: the large form.
	if s.large != nil {
		//: a flag.
		return s.large[i]
	}
	//: a bit.
	return s.small&(1<<uint(i)) != 0
}

// add records field i as set.
func (s *fieldSet) add(i int) {
	//: the large form.
	if s.large != nil {
		s.large[i] = true
		//: recorded.
		return
	}
	s.small |= 1 << uint(i)
}

// decode decodes a map into a struct, or an array into a toarray one.
func (structDecoder) decode(d *decodeState, v reflect.Value, p *decodePlan) error {
	h, done, err := d.begin(v, p)
	//: null, a BinaryUnmarshaler, or a broken walk.
	if done || err != nil {
		//: handled.
		return err
	}
	//: the struct's option decides the shape it takes.
	if p.fields.toArray {
		//: an array of its fields.
		return d.structFromArray(v, p, h)
	}
	//: a map of its fields.
	return d.structFromMap(v, p, h)
}

// structFromMap decodes the map h opens into the struct v.
func (d *decodeState) structFromMap(v reflect.Value, p *decodePlan, h itemHead) error {
	//: only a map fills a struct without toarray.
	if h.major != majorMap {
		//: recorded and skipped.
		return d.mismatch(h, p.typ)
	}
	d.off += h.size
	seen := newFieldSet(len(p.fields.list))
	seq := newSequence(h)
	//: pair after pair.
	for {
		more, err := d.next(&seq)
		//: the map is complete, or the walk is broken.
		if err != nil || !more {
			//: done.
			return err
		}
		//: never fails on validated input.
		if err := d.structEntry(v, p, &seen); err != nil {
			//: corrupt.
			return err
		}
	}
}

// structEntry decodes one pair into the field its key names, or skips it.
func (d *decodeState) structEntry(v reflect.Value, p *decodePlan, seen *fieldSet) error {
	idx, found, err := d.fieldIndex(p)
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return err
	}
	//: an unknown key, or one already set, is skipped with its value.
	if !found || seen.has(idx) {
		//: skipped.
		return d.skip()
	}
	seen.add(idx)
	//: into the field.
	return d.decodeField(v, &p.fields.list[idx])
}

// fieldIndex consumes a map key and finds the field it names. A key that
// cannot name a field — a byte string, a tag, an integer beyond int64 — is
// recorded as a failure and finds none.
func (d *decodeState) fieldIndex(p *decodePlan) (idx int, found bool, err error) {
	h, err := d.peek()
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return -1, false, err
	}
	//: text keys and integer keys name fields.
	switch h.major {
	case majorText:
		content, _, textErr := d.stringBytes(h)
		//: by name.
		return lookupText(p.fields, content, textErr)
	case majorUnsigned, majorNegative:
		d.off += h.size
		//: by integer.
		return d.intField(p, h)
	default:
		d.note(func() string {
			//: fxamacker/cbor refused it too.
			return "a map key that cannot name a field of " + p.typ.String()
		})
		//: skipped.
		return -1, false, d.skip()
	}
}

// lookupText finds the field the text key content names.
func lookupText(fs *decodeFields, content []byte, err error) (int, bool, error) {
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return -1, false, err
	}
	i, found := fs.byText(content)
	//: found or not.
	return i, found, nil
}

// intField finds the keyasint field the integer key h names.
func (d *decodeState) intField(p *decodePlan, h itemHead) (int, bool, error) {
	//: an integer key beyond int64 names no field.
	if h.arg > math.MaxInt64 {
		d.noteMismatch(h, p.typ)
		//: none.
		return -1, false, nil
	}
	n := int64(h.arg)
	//: −1−n for a negative key.
	if h.major == majorNegative {
		n = ^n
	}
	i, found := p.fields.byInt(n)
	//: found or not.
	return i, found, nil
}

// decodeField decodes the next item into the field f of the struct v,
// allocating the embedded pointers on its path.
func (d *decodeState) decodeField(v reflect.Value, f *decodeField) error {
	target, ok := fieldTarget(v, f.index)
	//: an unexported embedded pointer that is nil cannot be allocated.
	if !ok {
		d.note(func() string {
			//: fxamacker/cbor refused it too.
			return "the field " + f.path + " is promoted through a nil, unexported embedded pointer"
		})
		//: skipped.
		return d.skip()
	}
	outer := d.field
	d.field = f.path
	err := f.plan.kind.decode(d, target, f.plan)
	d.field = outer
	//: stored, or recorded.
	return err
}

// fieldTarget follows index from the struct v to a settable field,
// allocating every nil embedded pointer on the way. ok is false when one of
// them cannot be set.
func fieldTarget(v reflect.Value, index []int) (field reflect.Value, ok bool) {
	field = v.Field(index[0])
	//: one embedded level per remaining step.
	for _, step := range index[1:] {
		//: an embedded *T is allocated on demand.
		if field.Kind() == reflect.Pointer {
			//: nil and unexported: reflection may not set it.
			if field.IsNil() && !field.CanSet() {
				//: unreachable.
				return field, false
			}
			//: allocated.
			if field.IsNil() {
				field.Set(reflect.New(field.Type().Elem()))
			}
			field = field.Elem()
		}
		field = field.Field(step)
	}
	//: the field.
	return field, true
}

// structFromArray decodes the array h opens into a toarray struct, which
// requires exactly one element per field.
func (d *decodeState) structFromArray(v reflect.Value, p *decodePlan, h itemHead) error {
	//: only an array fills a toarray struct.
	if h.major != majorArray {
		//: recorded and skipped.
		return d.mismatch(h, p.typ)
	}
	start := d.off
	d.off += h.size
	count, err := d.entryCount(h)
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return err
	}
	//: one element per field, no more, no fewer.
	if count != len(p.fields.list) {
		d.off = start
		//: recorded and skipped.
		return d.mismatch(h, p.typ)
	}
	//: field by field, in order.
	for i := range p.fields.list {
		//: never fails on validated input.
		if err := d.decodeField(v, &p.fields.list[i]); err != nil {
			//: corrupt.
			return err
		}
	}
	//: an indefinite array's break is still to be read.
	return d.closeIndefinite(h)
}
