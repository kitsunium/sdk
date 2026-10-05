package cbor

import (
	"encoding"
	"reflect"
	"sync"

	"github.com/kitsunium/sdk/internal/kernel/concur/recycler"
)

// The properties a decode plan can have.
const (
	// planBinary is set when *T implements encoding.BinaryUnmarshaler: a
	// byte string is handed to UnmarshalBinary.
	planBinary planFlag = 1 << iota
	// planNullable is set for slices, maps, pointers and interfaces, which
	// null sets to nil; null leaves any other value as it is.
	planNullable
	// planKeyCheck is set when a map's key type can hold a value Go cannot
	// hash — an interface inside it — so each key is checked first.
	planKeyCheck
)

// Package-level caches and the interfaces the decoder honours.
var (
	// decodePlans maps a reflect.Type to its published *decodePlan.
	decodePlans sync.Map
	// binaryUnmarshalerType is encoding.BinaryUnmarshaler, handed a byte
	// string.
	binaryUnmarshalerType = reflect.TypeFor[encoding.BinaryUnmarshaler]()
	// cborUnmarshalerType is the method set fxamacker/cbor called an
	// Unmarshaler: a type that reads its own CBOR keeps doing so.
	cborUnmarshalerType = reflect.TypeFor[interface{ UnmarshalCBOR(data []byte) error }]()
	// scalarDecoders are the decoders of the kinds that need no other plan.
	scalarDecoders = map[reflect.Kind]decoder{
		reflect.Bool:    boolDecoder{},
		reflect.Int:     intDecoder{},
		reflect.Int8:    intDecoder{},
		reflect.Int16:   intDecoder{},
		reflect.Int32:   intDecoder{},
		reflect.Int64:   intDecoder{},
		reflect.Uint:    uintDecoder{},
		reflect.Uint8:   uintDecoder{},
		reflect.Uint16:  uintDecoder{},
		reflect.Uint32:  uintDecoder{},
		reflect.Uint64:  uintDecoder{},
		reflect.Float32: floatDecoder{},
		reflect.Float64: floatDecoder{},
		reflect.String:  stringDecoder{},
	}
)

// decoder stores data items into the values of one kind of plan.
type decoder interface {
	// decode stores the item at the cursor into v, a settable value of the
	// plan's type. It returns an error only when the walk itself breaks; an
	// item the value cannot hold is recorded on d and skipped.
	decode(d *decodeState, v reflect.Value, p *decodePlan) error
}

// boolDecoder stores true or false.
type boolDecoder struct{}

// intDecoder stores an integer into a signed integer of any width.
type intDecoder struct{}

// uintDecoder stores a non-negative integer into an unsigned integer.
type uintDecoder struct{}

// floatDecoder stores a float or an integer into a float32 or a float64.
type floatDecoder struct{}

// stringDecoder stores a text string.
type stringDecoder struct{}

// timeDecoder stores an RFC 3339 string or Unix seconds into a time.Time.
type timeDecoder struct{}

// bigIntDecoder stores an integer or a bignum into a big.Int.
type bigIntDecoder struct{}

// rawDecoder hands the whole item to a type's own UnmarshalCBOR.
type rawDecoder struct{}

// refusedDecoder records that nothing can be stored into the type.
type refusedDecoder struct{}

// pointerDecoder stores null as nil and anything else into the pointee.
type pointerDecoder struct{}

// emptyInterfaceDecoder stores the item's default Go value into an any.
type emptyInterfaceDecoder struct{}

// interfaceDecoder stores into the value an interface with methods holds.
type interfaceDecoder struct{}

// byteSliceDecoder stores bytes into a []byte.
type byteSliceDecoder struct{}

// byteArrayDecoder stores bytes into a [N]byte.
type byteArrayDecoder struct{}

// sliceDecoder stores an array into a slice.
type sliceDecoder struct{}

// arrayDecoder stores an array into a Go array.
type arrayDecoder struct{}

// mapDecoder stores a map into a Go map.
type mapDecoder struct{}

// structDecoder stores a map, or an array for toarray, into a struct.
type structDecoder struct{}

// decodePlan is how data items are stored into values of one Go type.
type decodePlan struct {
	// kind stores an item.
	kind decoder
	// typ is the planned type.
	typ reflect.Type
	// elem is the plan of a slice's, an array's or a map's elements, or of
	// the type a pointer points to.
	elem *decodePlan
	// key is the plan of a map's keys.
	key *decodePlan
	// fields are a struct's fields on the wire.
	fields *decodeFields
	// holders recycles the key and value a map's pairs are decoded into.
	holders *recycler.Pool[*mapHolders]
	// refusal says why no item can be stored into the type.
	refusal string
	// flags are the plan's properties.
	flags planFlag
}

// planFlag is one boolean property of a decode plan.
type planFlag uint8

// decodePlanner resolves the plans of one type graph, as encodePlanner does.
type decodePlanner struct {
	// local holds the plans of this resolution.
	local map[reflect.Type]*decodePlan
}

// decodePlanFor returns the plan of t, building and publishing it on first
// use.
func decodePlanFor(t reflect.Type) *decodePlan {
	//: the published plan, on every call but the first.
	if p, ok := loadDecodePlan(t); ok {
		//: cached.
		return p
	}
	planner := decodePlanner{local: map[reflect.Type]*decodePlan{}}
	root := planner.plan(t)
	//: every plan of the graph is complete: publish them all.
	for typ, p := range planner.local {
		decodePlans.LoadOrStore(typ, p)
	}
	//: the plan just built.
	return root
}

// loadDecodePlan returns the published plan of t, if any.
func loadDecodePlan(t reflect.Type) (*decodePlan, bool) {
	cached, ok := decodePlans.Load(t)
	//: not built yet.
	if !ok {
		//: none.
		return nil, false
	}
	plan, isPlan := cached.(*decodePlan)
	//: only *decodePlan values are ever stored.
	return plan, isPlan
}

// has reports whether the plan carries flag.
func (p *decodePlan) has(flag planFlag) bool {
	//: a bit test.
	return p.flags&flag != 0
}

// plan returns the plan of t: the published one, the one being built in
// this resolution, or a new one.
func (pl *decodePlanner) plan(t reflect.Type) *decodePlan {
	//: published by an earlier resolution.
	if p, ok := loadDecodePlan(t); ok {
		//: reused.
		return p
	}
	//: built, or being built, by this one.
	if p, ok := pl.local[t]; ok {
		//: a recursive type reaches its own plan here.
		return p
	}
	p := &decodePlan{typ: t}
	pl.local[t] = p
	pl.fill(p)
	//: complete.
	return p
}

// fill decides how items are stored into p.typ: pointers and interfaces
// first, then the types with a decoding of their own, then the kind.
func (pl *decodePlanner) fill(p *decodePlan) {
	t := p.typ
	//: the special types, in fxamacker/cbor's order of precedence.
	switch {
	case t.Kind() == reflect.Pointer:
		//: nil for null, the pointed-to value otherwise.
		pl.fillPointer(p)
	case t.Kind() == reflect.Interface:
		//: the default Go value, or the value an interface holds.
		p.kind, p.flags = interfaceKind(t), planNullable
	case t == timeType:
		//: RFC 3339 or Unix seconds.
		p.kind = timeDecoder{}
	case t == bigIntType:
		//: an integer or a bignum.
		p.kind = bigIntDecoder{}
	case reflect.PointerTo(t).Implements(cborUnmarshalerType):
		//: the type reads its own CBOR.
		p.kind = rawDecoder{}
	default:
		//: by kind; a byte string may go to UnmarshalBinary.
		//: a BinaryUnmarshaler reads its own binary form.
		if reflect.PointerTo(t).Implements(binaryUnmarshalerType) {
			p.flags |= planBinary
		}
		pl.fillKind(p)
	}
}

// interfaceKind picks the decoder of an interface type: the default Go
// value for an empty interface, the held pointer's value for any other.
func interfaceKind(t reflect.Type) decoder {
	//: any.
	if t.NumMethod() == 0 {
		//: built without reflection.
		return emptyInterfaceDecoder{}
	}
	//: an interface with methods cannot be given a value the codec invents.
	return interfaceDecoder{}
}

// fillKind plans a type by its kind.
func (pl *decodePlanner) fillKind(p *decodePlan) {
	//: the scalar kinds need no other plan.
	if decode, ok := scalarDecoders[p.typ.Kind()]; ok {
		p.kind = decode
		//: planned.
		return
	}
	//: the composite kinds.
	switch p.typ.Kind() {
	case reflect.Slice:
		p.elem = pl.plan(p.typ.Elem())
		p.flags |= planNullable
		p.kind = sliceKind(p.typ)
	case reflect.Array:
		p.elem = pl.plan(p.typ.Elem())
		p.kind = arrayKind(p.typ)
	case reflect.Map:
		pl.fillMap(p)
	case reflect.Struct:
		pl.fillStruct(p)
	default:
		//: chan, func, complex, uintptr, unsafe.Pointer.
		p.refusal, p.kind = "a value of a type CBOR cannot fill: "+p.typ.String(), refusedDecoder{}
	}
}

// sliceKind picks the decoder of a slice type.
func sliceKind(t reflect.Type) decoder {
	//: a byte slice takes a byte string, or an array of small integers.
	if t.Elem().Kind() == reflect.Uint8 {
		//: bytes.
		return byteSliceDecoder{}
	}
	//: an array of elements.
	return sliceDecoder{}
}

// arrayKind picks the decoder of an array type.
func arrayKind(t reflect.Type) decoder {
	//: a byte array takes a byte string, or an array of small integers.
	if t.Elem().Kind() == reflect.Uint8 {
		//: bytes.
		return byteArrayDecoder{}
	}
	//: an array of elements.
	return arrayDecoder{}
}

// fillPointer plans a pointer. A pointer type that points back to itself is
// refused: allocating along it would never end.
func (pl *decodePlanner) fillPointer(p *decodePlan) {
	p.flags |= planNullable
	//: type P *P can only ever be nil or a cycle.
	if pointerCycle(p.typ) {
		p.refusal, p.kind = "a value of a self-referential pointer type: "+p.typ.String(), refusedDecoder{}
		//: refused.
		return
	}
	p.elem, p.kind = pl.plan(p.typ.Elem()), pointerDecoder{}
}

// fillMap plans a map from the plans of its key and value types.
func (pl *decodePlanner) fillMap(p *decodePlan) {
	p.key, p.elem = pl.plan(p.typ.Key()), pl.plan(p.typ.Elem())
	p.flags |= planNullable
	//: a key type with an interface inside is checked key by key.
	if mayHoldUnhashable(p.typ.Key()) {
		p.flags |= planKeyCheck
	}
	p.holders = newHolders(p.typ)
	p.kind = mapDecoder{}
}

// mayHoldUnhashable reports whether a value of the comparable type t can
// still hold something Go cannot hash: an interface, directly or inside an
// array or a struct.
func mayHoldUnhashable(t reflect.Type) bool {
	//: only an interface can hold a slice or a map in a comparable type.
	switch t.Kind() {
	case reflect.Interface:
		//: any dynamic value.
		return true
	case reflect.Array:
		//: through its elements.
		return mayHoldUnhashable(t.Elem())
	case reflect.Struct:
		//: through any field.
		return structMayHoldUnhashable(t)
	default:
		//: fixed, comparable.
		return false
	}
}

// structMayHoldUnhashable reports whether any field of t may hold a value
// Go cannot hash.
func structMayHoldUnhashable(t reflect.Type) bool {
	//: field by field.
	for f := range t.Fields() {
		//: one is enough.
		if mayHoldUnhashable(f.Type) {
			//: checked at run time.
			return true
		}
	}
	//: always hashable.
	return false
}

// hashable reports whether v can be a Go map key: nothing inside it is a
// slice, a map or a func.
func hashable(v reflect.Value) bool {
	//: only what can hold another value needs a look inside.
	switch v.Kind() {
	case reflect.Interface:
		//: nil is hashable; otherwise the dynamic value decides.
		return v.IsNil() || hashable(v.Elem())
	case reflect.Slice, reflect.Map, reflect.Func:
		//: not comparable.
		return false
	case reflect.Array:
		//: every element.
		return allHashable(v.Len(), v.Index)
	case reflect.Struct:
		//: every field.
		return allHashable(v.NumField(), v.Field)
	default:
		//: a scalar, a string, a pointer, a channel.
		return true
	}
}

// allHashable reports whether the n values part returns are all hashable.
func allHashable(n int, part func(int) reflect.Value) bool {
	//: each one.
	for i := range n {
		//: one is enough to refuse.
		if !hashable(part(i)) {
			//: not hashable.
			return false
		}
	}
	//: all of them.
	return true
}

// begin positions the cursor on the item a kind plan stores: past every tag
// it does not interpret — all but the bignums — and it deals with the cases
// every kind shares: null, which zeroes a nullable value and leaves any
// other as it is, and a byte string for a BinaryUnmarshaler. done reports
// that nothing is left to do.
func (d *decodeState) begin(v reflect.Value, p *decodePlan) (h itemHead, done bool, err error) {
	h, err = d.skipTags()
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return h, true, err
	}
	//: null and undefined.
	if isNull(h) {
		d.off += h.size
		//: only a nullable value is touched.
		if p.has(planNullable) {
			v.SetZero()
		}
		//: handled.
		return h, true, nil
	}
	//: a byte string for a type that reads its own binary form.
	if h.major == majorBytes && p.has(planBinary) {
		//: handed over.
		return h, true, d.unmarshalBinary(v, p, h)
	}
	//: the item for the kind plan.
	return h, false, nil
}

// skipTags consumes the tag heads at the cursor, stopping at a bignum's,
// which integer and byte targets read, and returns the head that follows.
func (d *decodeState) skipTags() (itemHead, error) {
	//: an unrecognised tag is transparent.
	for {
		h, err := d.peek()
		//: the first head that is not a transparent tag.
		if err != nil || h.major != majorTag || h.arg == tagPositiveBignum || h.arg == tagNegativeBignum {
			//: the item.
			return h, err
		}
		d.off += h.size
	}
}

// unmarshalBinary hands the byte string h opens to v's UnmarshalBinary.
func (d *decodeState) unmarshalBinary(v reflect.Value, p *decodePlan, h itemHead) error {
	content, _, err := d.stringBytes(h)
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return err
	}
	u, ok := addressedMethods[encoding.BinaryUnmarshaler](v)
	//: a value without an address cannot be written through a method.
	if !ok {
		d.noteMismatch(h, p.typ)
		//: recorded.
		return nil
	}
	//: the content aliases the input; the method's contract forbids keeping it.
	if unmarshalErr := u.UnmarshalBinary(content); unmarshalErr != nil {
		d.noteCause(unmarshalErr, "UnmarshalBinary failed for "+p.typ.String())
	}
	//: stored, or recorded.
	return nil
}

// noteCause records a failure returned by a type's own decoding method.
func (d *decodeState) noteCause(cause error, detail string) {
	d.failures++
	//: the first failure is the one reported, with its cause.
	if d.first == nil {
		d.first = decodeCause(cause, detail)
	}
}

// addressedMethods returns v's address as the interface I, when v has one.
func addressedMethods[I any](v reflect.Value) (I, bool) {
	//: decoding writes through the value's address.
	if !v.CanAddr() {
		var none I
		//: not addressable.
		return none, false
	}
	//: the pointer method set includes the value one.
	return reflect.TypeAssert[I](v.Addr())
}

// decode hands a type with its own UnmarshalCBOR the whole item, tags and
// null included, as fxamacker/cbor did.
func (rawDecoder) decode(d *decodeState, v reflect.Value, p *decodePlan) error {
	start := d.off
	//: the item's extent.
	if err := d.skip(); err != nil {
		//: corrupt.
		return err
	}
	u, ok := addressedMethods[interface{ UnmarshalCBOR(data []byte) error }](v)
	//: a value without an address cannot be written through a method.
	if !ok {
		d.note(func() string {
			//: the type.
			return "cannot call UnmarshalCBOR on an unaddressable " + p.typ.String()
		})
		//: recorded.
		return nil
	}
	//: the bytes alias the input; the method must copy what it keeps.
	if err := u.UnmarshalCBOR(d.data[start:d.off]); err != nil {
		d.noteCause(err, "UnmarshalCBOR failed for "+p.typ.String())
	}
	//: stored, or recorded.
	return nil
}

// decode records that nothing can be stored into the plan's type;
// null still leaves it as it is.
func (refusedDecoder) decode(d *decodeState, v reflect.Value, p *decodePlan) error {
	_, done, err := d.begin(v, p)
	//: null, or a broken walk.
	if done || err != nil {
		//: handled.
		return err
	}
	d.note(func() string {
		//: the reason the plan was refused.
		return p.refusal
	})
	//: skipped.
	return d.skip()
}

// decode stores null as a nil pointer, and anything else into the
// pointed-to value, allocating it when the pointer is nil.
func (pointerDecoder) decode(d *decodeState, v reflect.Value, p *decodePlan) error {
	h, err := d.peekPastTags()
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return err
	}
	//: null, tagged or not, is a nil pointer.
	if isNull(h) {
		v.SetZero()
		//: the tags and the null.
		return d.skip()
	}
	//: allocated on demand; an existing value is decoded into.
	if v.IsNil() {
		v.Set(reflect.New(p.typ.Elem()))
	}
	//: the pointed-to value sees the tags too.
	return p.elem.kind.decode(d, v.Elem(), p.elem)
}

// peekPastTags returns the head of the item the tags at the cursor enclose,
// consuming nothing.
func (d *decodeState) peekPastTags() (itemHead, error) {
	off := d.off
	//: tag after tag.
	for {
		h, ok := readHead(d.data, off)
		//: never on validated input.
		if !ok {
			//: corrupt.
			return h, d.corrupt()
		}
		//: the first head that is not a tag.
		if h.major != majorTag {
			//: the enclosed item.
			return h, nil
		}
		off += h.size
	}
}

// decode stores the item's default Go value into an empty
// interface; null sets it to nil.
func (emptyInterfaceDecoder) decode(d *decodeState, v reflect.Value, _ *decodePlan) error {
	x, err := d.decodeAny()
	//: nil — null, or an item that could not be decoded.
	if x == nil {
		v.SetZero()
		//: corrupt, or done.
		return err
	}
	v.Set(reflect.ValueOf(x))
	//: stored.
	return err
}

// decode stores an item into an interface with methods: null sets
// it to nil; anything else goes into the value its non-nil pointer points
// to. The codec cannot invent a value that satisfies the interface.
func (interfaceDecoder) decode(d *decodeState, v reflect.Value, p *decodePlan) error {
	h, err := d.peekPastTags()
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return err
	}
	//: null is a nil interface.
	if isNull(h) {
		v.SetZero()
		//: the tags and the null.
		return d.skip()
	}
	held := v.Elem()
	//: only a held, non-nil pointer can be written through.
	if v.IsNil() || held.Kind() != reflect.Pointer || held.IsNil() {
		//: recorded and skipped.
		return d.mismatch(h, p.typ)
	}
	plan := decodePlanFor(held.Type().Elem())
	//: through the dynamic type's plan.
	return plan.kind.decode(d, held.Elem(), plan)
}
