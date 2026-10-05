package cbor

import (
	"encoding"
	"math/big"
	"reflect"
	"sync"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/concur/recycler"
)

// Package-level caches and the types the planner recognises.
var (
	// encodePlans maps a reflect.Type to its published *encodePlan.
	encodePlans sync.Map
	// timeType is time.Time, encoded as Unix seconds.
	timeType = reflect.TypeFor[time.Time]()
	// bigIntType is big.Int, encoded as an integer or a bignum.
	bigIntType = reflect.TypeFor[big.Int]()
	// binaryMarshalerType is encoding.BinaryMarshaler, encoded as a byte string.
	binaryMarshalerType = reflect.TypeFor[encoding.BinaryMarshaler]()
	// cborMarshalerType is the method set fxamacker/cbor called a Marshaler:
	// a type that writes its own CBOR keeps doing so.
	cborMarshalerType = reflect.TypeFor[interface{ MarshalCBOR() ([]byte, error) }]()
	// isZeroerType is the method omitzero consults when a type declares it.
	isZeroerType = reflect.TypeFor[interface{ IsZero() bool }]()
	// scalarEncoders are the kind encoders that need no other plan.
	scalarEncoders = map[reflect.Kind]kindEncoder{
		reflect.Bool:    boolEncoder{},
		reflect.Int:     intEncoder{},
		reflect.Int8:    intEncoder{},
		reflect.Int16:   intEncoder{},
		reflect.Int32:   intEncoder{},
		reflect.Int64:   intEncoder{},
		reflect.Uint:    uintEncoder{},
		reflect.Uint8:   uintEncoder{},
		reflect.Uint16:  uintEncoder{},
		reflect.Uint32:  uintEncoder{},
		reflect.Uint64:  uintEncoder{},
		reflect.Float32: floatEncoder{},
		reflect.Float64: floatEncoder{},
		reflect.String:  stringEncoder{},
	}
)

// kindEncoder encodes the values of one kind of plan.
type kindEncoder interface {
	// encode appends the encoding of v, a value of the plan's type, to b.
	encode(b []byte, v reflect.Value, p *encodePlan, at walkDepth) ([]byte, error)
	// empty reports whether v encodes as an empty CBOR item — false, 0,
	// 0.0, an empty string or container, null — which omitempty omits.
	empty(v reflect.Value, p *encodePlan) (bool, error)
}

// zeroFunc reports whether v is its type's zero value, which omitzero omits.
type zeroFunc func(v reflect.Value) (bool, error)

// encodePlan is how the values of one Go type are encoded.
type encodePlan struct {
	// kind encodes a value and answers omitempty.
	kind kindEncoder
	// zero answers omitzero.
	zero zeroFunc
	// typ is the planned type.
	typ reflect.Type
	// elem is the plan of a slice's, an array's or a map's elements, or of
	// the type a pointer points to.
	elem *encodePlan
	// key is the plan of a map's keys.
	key *encodePlan
	// holders recycles the key and value a map's pairs are copied into.
	holders *recycler.Pool[*mapHolders]
	// fields are a struct's fields on the wire.
	fields []encodeField
	// refusal says why no value of the type can be encoded.
	refusal string
	// omittable counts the struct fields carrying omitempty.
	omittable int
}

// encodeField is one struct field as the encoder writes it.
type encodeField struct {
	// key is the field's encoded key: a text string, or an integer.
	key []byte
	// index is the path from the struct to the field.
	index []int
	// plan encodes the field's value.
	plan *encodePlan
	// flags are the field's options.
	flags fieldFlag
}

// mapHolders is a map key and a map value, reused across the pairs of the
// maps of one type.
type mapHolders struct {
	// key receives each pair's key.
	key reflect.Value
	// value receives each pair's value.
	value reflect.Value
}

// encodePlanner resolves the plans of one type graph. Plans are built in a
// private map, so a recursive type finds its own plan while it is being
// built, and published only once every plan of the graph is complete.
type encodePlanner struct {
	// local holds the plans of this resolution.
	local map[reflect.Type]*encodePlan
}

// encodePlanFor returns the plan of t, building and publishing it on first
// use.
func encodePlanFor(t reflect.Type) *encodePlan {
	//: the published plan, on every call but the first.
	if p, ok := loadEncodePlan(t); ok {
		//: cached.
		return p
	}
	planner := encodePlanner{local: map[reflect.Type]*encodePlan{}}
	root := planner.plan(t)
	//: every plan of the graph is complete: publish them all.
	for typ, p := range planner.local {
		encodePlans.LoadOrStore(typ, p)
	}
	//: the plan just built.
	return root
}

// loadEncodePlan returns the published plan of t, if any.
func loadEncodePlan(t reflect.Type) (*encodePlan, bool) {
	cached, ok := encodePlans.Load(t)
	//: not built yet.
	if !ok {
		//: none.
		return nil, false
	}
	plan, isPlan := cached.(*encodePlan)
	//: only *encodePlan values are ever stored.
	return plan, isPlan
}

// plan returns the plan of t: the published one, the one being built in
// this resolution, or a new one.
func (pl *encodePlanner) plan(t reflect.Type) *encodePlan {
	//: published by an earlier resolution.
	if p, ok := loadEncodePlan(t); ok {
		//: reused.
		return p
	}
	//: built, or being built, by this one.
	if p, ok := pl.local[t]; ok {
		//: a recursive type reaches its own plan here.
		return p
	}
	p := &encodePlan{typ: t, zero: zeroFuncFor(t)}
	pl.local[t] = p
	pl.fill(p)
	//: complete.
	return p
}

// fill decides how values of p.typ are encoded. Pointers come first, then
// the types with an encoding of their own, then the kind.
func (pl *encodePlanner) fill(p *encodePlan) {
	t := p.typ
	//: the special types, in fxamacker/cbor's order of precedence.
	switch {
	case t.Kind() == reflect.Pointer:
		//: null or the pointed-to value.
		pl.fillPointer(p)
	case t == timeType:
		//: Unix seconds; never "empty", as before.
		p.kind = timeEncoder{}
	case t == bigIntType:
		//: an integer or a bignum; never "empty".
		p.kind = bigIntEncoder{}
	case reflect.PointerTo(t).Implements(cborMarshalerType):
		//: the type's own CBOR; never "empty".
		p.kind = cborMarshalerEncoder{}
	case reflect.PointerTo(t).Implements(binaryMarshalerType):
		//: a byte string; "empty" when MarshalBinary returns nothing.
		p.kind = binaryMarshalerEncoder{}
	default:
		//: by kind.
		pl.fillKind(p)
	}
}

// fillKind plans a type by its kind.
func (pl *encodePlanner) fillKind(p *encodePlan) {
	//: the scalar kinds need no other plan.
	if scalar, ok := scalarEncoders[p.typ.Kind()]; ok {
		p.kind = scalar
		//: planned.
		return
	}
	//: the composite kinds.
	switch p.typ.Kind() {
	case reflect.Slice, reflect.Array:
		//: a byte string or an array.
		pl.fillSequence(p)
	case reflect.Map:
		//: a map with sorted pairs.
		pl.fillMap(p)
	case reflect.Struct:
		//: a map keyed by field, or an array.
		pl.fillStruct(p)
	case reflect.Interface:
		//: null or the dynamic value.
		p.kind = interfaceEncoder{}
	default:
		//: chan, func, complex, uintptr, unsafe.Pointer.
		refuse(p, "a value of a type CBOR cannot carry: "+p.typ.String())
	}
}

// fillPointer plans a pointer: null when nil, the pointed-to value otherwise.
// A pointer type that points back to itself refuses every value, and so does
// a pointer to a type CBOR cannot carry.
func (pl *encodePlanner) fillPointer(p *encodePlan) {
	//: type P *P can only ever be nil or a cycle.
	if pointerCycle(p.typ) {
		refuse(p, "a value of a self-referential pointer type: "+p.typ.String())
		//: refused.
		return
	}
	p.elem = pl.plan(p.typ.Elem())
	//: the pointed-to type's refusal is the pointer's.
	if p.elem.refusal != "" {
		refuse(p, p.elem.refusal)
		//: refused.
		return
	}
	p.kind = pointerEncoder{}
}

// fillSequence plans a slice or an array: a byte string when its elements
// are bytes, an array of encoded elements otherwise.
func (pl *encodePlanner) fillSequence(p *encodePlan) {
	//: []byte, [N]byte and their named forms are byte strings.
	if p.typ.Elem().Kind() == reflect.Uint8 {
		p.kind = byteSequenceEncoder{}
		//: no element plan needed.
		return
	}
	p.elem = pl.plan(p.typ.Elem())
	//: an element type CBOR cannot carry refuses the sequence.
	if p.elem.refusal != "" {
		refuse(p, p.elem.refusal)
		//: refused.
		return
	}
	p.kind = sequenceEncoder{}
}

// fillMap plans a map from the plans of its key and value types.
func (pl *encodePlanner) fillMap(p *encodePlan) {
	p.key, p.elem = pl.plan(p.typ.Key()), pl.plan(p.typ.Elem())
	//: a key type CBOR cannot carry refuses the map.
	if p.key.refusal != "" {
		refuse(p, p.key.refusal)
		//: refused.
		return
	}
	//: so does a value type.
	if p.elem.refusal != "" {
		refuse(p, p.elem.refusal)
		//: refused.
		return
	}
	p.holders = newHolders(p.typ)
	p.kind = mapEncoder{}
}

// newHolders returns the pool of key and value holders of the map type t.
func newHolders(t reflect.Type) *recycler.Pool[*mapHolders] {
	keyType, valueType := t.Key(), t.Elem()
	//: settable, zeroed values of the map's key and value types.
	return recycler.NewPool(func() *mapHolders {
		//: one of each.
		return &mapHolders{key: reflect.New(keyType).Elem(), value: reflect.New(valueType).Elem()}
	})
}

// releaseHolders clears the holders, so the pool keeps no reference to a
// map's keys and values, and returns them to pool.
func releaseHolders(pool *recycler.Pool[*mapHolders], holders *mapHolders) {
	holders.key.SetZero()
	holders.value.SetZero()
	pool.Put(holders)
}

// refuse makes p a plan that encodes nothing and says why.
func refuse(p *encodePlan, refusal string) {
	p.refusal = refusal
	p.kind = refusedEncoder{}
}

// pointerCycle reports whether following t's pointer chain comes back to t,
// or does not end within maxIndirections levels.
func pointerCycle(t reflect.Type) bool {
	elem := t.Elem()
	//: walk the chain of pointer types.
	for range maxIndirections {
		//: the chain ends at a type that is not a pointer.
		if elem.Kind() != reflect.Pointer {
			//: finite.
			return false
		}
		//: back where it started.
		if elem == t {
			//: a cycle.
			return true
		}
		elem = elem.Elem()
	}
	//: no type has that many levels of pointer on purpose.
	return true
}

// asMethods returns v as the interface I, through v's address when I's
// methods have pointer receivers, and through an addressable copy when v
// itself is not addressable.
func asMethods[I any](v reflect.Value) (I, bool) {
	//: an addressable value has every method, value or pointer receiver.
	if v.CanAddr() {
		//: no copy.
		return reflect.TypeAssert[I](v.Addr())
	}
	//: value receivers are reachable on the value itself.
	if m, ok := reflect.TypeAssert[I](v); ok {
		//: no copy.
		return m, true
	}
	copied := reflect.New(v.Type())
	copied.Elem().Set(v)
	//: pointer receivers need an address: the copy's.
	return reflect.TypeAssert[I](copied)
}
