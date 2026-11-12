package otlp

import coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"

// Arena hands out the attribute sets and the oneof values of one export from
// a few slices sized once, instead of a slice per set and a pointer per value
// (#813): what Attrs and KeyValueOf allocate per point, an Arena allocates
// per export. What it hands out is exactly what Attrs and KeyValueOf would
// build — the same KeyValues, the same oneof case — so a tree built through
// it renders byte for byte the same. Each set is a full slice expression:
// an append to it never writes into the next. Not safe for concurrent use;
// one export, one goroutine.
type Arena struct {
	// pairs backs every attribute set handed out.
	pairs []KeyValue
	// strings, bools, ints and doubles back the oneof values, each allocated
	// on its kind's first use with the arena's whole capacity.
	strings []string
	bools   []bool
	ints    []Int64
	doubles []Double
	// capacity is what a kind's slice is allocated with: every attribute and
	// every extra value could be of that kind.
	capacity int
}

// NewArena returns an Arena for one export of at most attrs attributes in
// all and values more oneof values (a point's asInt or asDouble), its slices
// sized up front. A count too small costs a slice grown, never a wrong
// tree.
func NewArena(attrs, values int) *Arena {
	//: the sets' backing array, once; the values' arrays on first use.
	return &Arena{pairs: make([]KeyValue, 0, max(attrs, 0)), capacity: max(attrs, 0) + max(values, 0)}
}

// Attrs is the package's Attrs, the KeyValues and their values taken
// from the arena. An empty set stays nil.
func (a *Arena) Attrs(attrs []coreotel.AttrValue) []KeyValue {
	//: the dimensionless case has no attributes array at all.
	if len(attrs) == 0 {
		//: omitted, as Attrs omits it.
		return nil
	}
	start := len(a.pairs)
	//: one KeyValue per dimension, in the payload's canonical order.
	for _, attr := range attrs {
		a.pairs = append(a.pairs, a.keyValue(attr))
	}
	//: capped at its own end, so the next set never shows through it.
	return a.pairs[start:len(a.pairs):len(a.pairs)]
}

// keyValue is KeyValueOf with the oneof value held in the arena.
func (a *Arena) keyValue(attr coreotel.AttrValue) KeyValue {
	//: one oneof case per attribute kind, as KeyValueOf chooses it.
	switch attr.Kind() {
	//: the common dimension.
	case coreotel.AttrKindString:
		a.strings = grow(a.strings, a.capacity)
		a.strings = append(a.strings, attr.Str())
		//: stringValue.
		return KeyValue{Key: attr.Key, Value: AnyValue{StringValue: &a.strings[len(a.strings)-1]}}
	//: a flag.
	case coreotel.AttrKindBool:
		a.bools = grow(a.bools, a.capacity)
		a.bools = append(a.bools, attr.Bool())
		//: boolValue — false is a value, not an absence.
		return KeyValue{Key: attr.Key, Value: AnyValue{BoolValue: &a.bools[len(a.bools)-1]}}
	//: a signed 64-bit integer, which rides as a decimal string.
	case coreotel.AttrKindInt64:
		//: intValue.
		return KeyValue{Key: attr.Key, Value: AnyValue{IntValue: a.Int64(attr.Int64())}}
	//: an IEEE-754 double, non-finite values included.
	case coreotel.AttrKindFloat64:
		//: doubleValue.
		return KeyValue{Key: attr.Key, Value: AnyValue{DoubleValue: a.Double(attr.Float64())}}
	//: AttrKindInvalid never reaches a payload, as in KeyValueOf.
	default:
		//: no case selected.
		return KeyValue{Key: attr.Key}
	}
}

// Int64 returns a pointer to v, held in the arena.
func (a *Arena) Int64(v int64) *Int64 {
	a.ints = grow(a.ints, a.capacity)
	a.ints = append(a.ints, Int64(v))
	//: the element just appended.
	return &a.ints[len(a.ints)-1]
}

// Double returns a pointer to v, held in the arena.
func (a *Arena) Double(v float64) *Double {
	a.doubles = grow(a.doubles, a.capacity)
	a.doubles = append(a.doubles, Double(v))
	//: the element just appended.
	return &a.doubles[len(a.doubles)-1]
}

// grow allocates a kind's slice with the arena's capacity on its first use,
// and returns it unchanged after.
func grow[T any](values []T, capacity int) []T {
	//: the kind's first value: one array for every value it could have.
	if values == nil {
		values = make([]T, 0, max(capacity, 1))
	}
	//: allocated, once.
	return values
}
