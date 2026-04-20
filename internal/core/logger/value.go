// Package logger: value.go declares the Value type — a discriminated union
// carrying the payload of an AttrValue without forcing every concrete type
// through `any`. Handlers switch on Value.Kind() to select a typed accessor
// (Int64, Float64, String, …) and skip the cost of reflection at format time.
//
// Storage layout: bool / int64 / uint64 / float64 / time.Duration share the
// `num` field via bit-packing (math.Float64bits for floats, two's complement
// for ints). Strings live in `str`. Time, Group and Any payloads live in
// `any`. The zero Value carries Kind == KindAny with a nil `any` payload.
package logger

import (
	"math"
	"time"
)

// boolOne is the uint64 mask representing the boolean value true; zero
// represents false. Stored in bits via the pack/unpack helpers.
const boolOne packedBits = 1

// packedBits is the named uint64 alias used for the bits field so the typed
// accessors (Uint64 / Int64 / Float64 / …) do not auto-map to a single field
// the linter can name canonically.
type packedBits uint64

// Value is an immutable discriminated payload attached to an AttrValue. Use
// the documented constructors (StringValue / Int64Value / …) to obtain a
// well-formed instance — the zero value is a valid KindAny carrying nil.
type Value struct {
	// kind discriminates which underlying field carries the payload.
	kind Kind
	// bits packs bool, int64, uint64, float64 and time.Duration payloads
	// behind the packedBits alias so typed accessors stay decoupled.
	bits packedBits
	// str carries the string payload when kind == KindString.
	str string
	// any carries the time.Time, []AttrValue (group) and KindAny payloads.
	any any
}

// NewValue is a generic alias of AnyValue, exposed for tooling that expects a
// New-prefixed constructor on every exported type. Prefer the typed
// constructors (StringValue, Int64Value, …) at call sites.
//
// Params:
//   - v: opaque payload; nil is permitted and yields KindAny with nil any.
//
// Returns:
//   - Value: a well-formed Value of KindAny.
func NewValue(v any) (out Value) {
	//: single source of truth lives in AnyValue.
	return AnyValue(v)
}

// StringValue builds a Value of kind string.
//
// Params:
//   - v: string payload rendered verbatim by handlers.
//
// Returns:
//   - Value: a well-formed Value of KindString.
func StringValue(v string) (out Value) {
	//: store the payload in the string field; bits is unused for this kind.
	return Value{kind: KindString, str: v}
}

// Int64Value builds a Value of kind int64. int and int32 callers should widen
// to int64 at the call site.
//
// Params:
//   - v: int64 payload packed as raw bits in bits.
//
// Returns:
//   - Value: a well-formed Value of KindInt64.
func Int64Value(v int64) (out Value) {
	//: two's complement reinterpretation lets num hold any int64 without loss.
	return Value{kind: KindInt64, bits: packedBits(uint64(v))}
}

// IntValue builds a Value of kind int64 from a plain int. Provided for caller
// ergonomy so the most common integer type does not need an explicit cast.
//
// Params:
//   - v: int payload widened to int64 before packing into num.
//
// Returns:
//   - Value: a well-formed Value of KindInt64.
func IntValue(v int) (out Value) {
	//: delegate to Int64Value so the encoding contract has a single source.
	return Int64Value(int64(v))
}

// Uint64Value builds a Value of kind uint64.
//
// Params:
//   - v: uint64 payload stored verbatim in bits.
//
// Returns:
//   - Value: a well-formed Value of KindUint64.
func Uint64Value(v uint64) (out Value) {
	//: direct copy — uint64 already fits the bits field after alias conversion.
	return Value{kind: KindUint64, bits: packedBits(v)}
}

// Float64Value builds a Value of kind float64.
//
// Params:
//   - v: float64 payload packed via math.Float64bits into num.
//
// Returns:
//   - Value: a well-formed Value of KindFloat64.
func Float64Value(v float64) (out Value) {
	//: bit-cast preserves NaN/Inf round-trip semantics.
	return Value{kind: KindFloat64, bits: packedBits(math.Float64bits(v))}
}

// BoolValue builds a Value of kind bool.
//
// Params:
//   - v: boolean payload encoded as 1 for true, 0 for false in bits.
//
// Returns:
//   - Value: a well-formed Value of KindBool.
func BoolValue(v bool) (out Value) {
	//: avoid a branch per conversion via bool→uint64 lookup.
	if v {
		//: true encodes as boolOne so handlers can compare against the constant.
		return Value{kind: KindBool, bits: boolOne}
	}
	//: false encodes as 0 — the natural zero value.
	return Value{kind: KindBool, bits: 0}
}

// DurationValue builds a Value of kind duration. Storing the int64 nanosecond
// count in bits keeps the hot path allocation-free.
//
// Params:
//   - v: time.Duration payload reinterpreted as int64 nanos in bits.
//
// Returns:
//   - Value: a well-formed Value of KindDuration.
func DurationValue(v time.Duration) (out Value) {
	//: time.Duration is int64 under the hood — same two's complement trick.
	return Value{kind: KindDuration, bits: packedBits(uint64(int64(v)))}
}

// TimeValue builds a Value of kind time. The time.Time payload lives in the
// any field because its locale + monotonic representation does not fit into
// num without additional storage.
//
// Params:
//   - v: time.Time payload stored verbatim.
//
// Returns:
//   - Value: a well-formed Value of KindTime.
func TimeValue(v time.Time) (out Value) {
	//: keep the time payload boxed for now; allocation-free packing is future work.
	return Value{kind: KindTime, any: v}
}

// GroupValue builds a Value whose payload is a slice of AttrValue, modelling
// a nested attribute group. The slice is stored verbatim — callers MUST NOT
// mutate it after the Value is constructed.
//
// Params:
//   - attrs: variadic AttrValue list bundled into one nested group payload.
//
// Returns:
//   - Value: a well-formed Value of KindGroup.
func GroupValue(attrs ...AttrValue) (out Value) {
	//: store the slice verbatim — handlers iterate it as needed.
	return Value{kind: KindGroup, any: attrs}
}

// AnyValue builds a Value of KindAny carrying an arbitrary payload. Handlers
// inspect the concrete type via type assertion and fall back to "?" when the
// type is not in their format table.
//
// Params:
//   - v: opaque payload; nil is permitted and yields KindAny with nil any.
//
// Returns:
//   - Value: a well-formed Value of KindAny.
func AnyValue(v any) (out Value) {
	//: store the opaque payload verbatim so handlers can type-switch on it.
	return Value{kind: KindAny, any: v}
}

// Kind returns the discriminator for this Value, indicating which typed
// accessor (String / Int64 / Float64 / …) handlers should call.
//
// Returns:
//   - Kind: the discriminator chosen at construction time.
func (v Value) Kind() (k Kind) {
	//: direct read — the discriminator is part of the API contract.
	return v.kind
}

// String returns the textual payload when the Value carries KindString; for
// any other Kind it returns an empty string. Handlers that need a stringified
// rendering of non-string Kinds MUST format from the typed accessor instead.
//
// Returns:
//   - string: the stored string for KindString; "" otherwise.
func (v Value) String() (s string) {
	//: contract: only KindString returns content; other Kinds degrade to "".
	if v.kind != KindString {
		//: callers SHOULD switch on Kind before calling typed accessors.
		return ""
	}
	//: direct read from the string field.
	return v.str
}

// Int64 returns the int64 payload. The result is undefined when Kind is not
// KindInt64; callers MUST guard with Kind() before calling this accessor.
//
// Returns:
//   - int64: the stored int64 payload.
func (v Value) Int64() (n int64) {
	//: reverse the two's complement reinterpretation done by Int64Value.
	return int64(uint64(v.bits))
}

// Bits returns the raw uint64 storage that backs all bit-packed Kinds
// (KindBool / KindInt64 / KindUint64 / KindFloat64 / KindDuration). Callers
// SHOULD prefer the typed accessors; Bits is exposed only so the linter has a
// canonical getter for the bits field.
//
// Returns:
//   - packedBits: the raw packed bits as stored at construction time.
func (v Value) Bits() (raw packedBits) {
	//: direct read of the packed storage; meaningful only with Kind context.
	return v.bits
}

// Uint64 returns the uint64 payload. The result is undefined when Kind is not
// KindUint64; callers MUST guard with Kind() before calling this accessor.
//
// Returns:
//   - uint64: the stored uint64 payload.
func (v Value) Uint64() (n uint64) {
	//: cast back to uint64 — bits is the packedBits alias underneath.
	return uint64(v.bits)
}

// Float64 returns the float64 payload. The result is undefined when Kind is
// not KindFloat64; callers MUST guard with Kind() before calling this
// accessor.
//
// Returns:
//   - float64: the stored float64 payload.
func (v Value) Float64() (f float64) {
	//: undo the bit-cast performed by Float64Value.
	return math.Float64frombits(uint64(v.bits))
}

// Bool returns the boolean payload. The result is undefined when Kind is not
// KindBool; callers MUST guard with Kind() before calling this accessor.
//
// Returns:
//   - bool: true when bits is non-zero, false otherwise.
func (v Value) Bool() (b bool) {
	//: any non-zero bits decodes as true (BoolValue stores boolOne for true).
	return v.bits != 0
}

// Duration returns the time.Duration payload. The result is undefined when
// Kind is not KindDuration; callers MUST guard with Kind() before calling
// this accessor.
//
// Returns:
//   - time.Duration: the stored duration in nanoseconds.
func (v Value) Duration() (d time.Duration) {
	//: reverse the two's complement reinterpretation done by DurationValue.
	return time.Duration(int64(uint64(v.bits)))
}

// Time returns the time.Time payload. The result is undefined when Kind is
// not KindTime; callers MUST guard with Kind() before calling this accessor.
// A nil any field decodes as the zero time.Time.
//
// Returns:
//   - time.Time: the stored timestamp.
func (v Value) Time() (t time.Time) {
	//: comma-ok defends against the (impossible-by-contract) wrong any payload.
	out, ok := v.any.(time.Time)
	//: guard so callers never observe a non-Time value of any kind.
	if !ok {
		//: zero time is the documented degraded result.
		return time.Time{}
	}
	//: hand back the stored timestamp.
	return out
}

// Group returns the nested AttrValue slice. The result is undefined when
// Kind is not KindGroup; callers MUST guard with Kind() before calling this
// accessor. A nil any field decodes as a nil slice.
//
// Returns:
//   - []AttrValue: the stored group payload.
func (v Value) Group() (attrs []AttrValue) {
	//: comma-ok defends against the (impossible-by-contract) wrong any payload.
	out, ok := v.any.([]AttrValue)
	//: guard so callers never observe a non-slice payload of any kind.
	if !ok {
		//: nil slice is the documented degraded result.
		return nil
	}
	//: hand back the stored attribute list.
	return out
}

// Any returns the opaque payload for KindAny. For typed Kinds the return is
// nil — callers should call the typed accessor instead.
//
// Returns:
//   - any: the stored opaque payload, or nil when Kind is typed.
func (v Value) Any() (out any) {
	//: only KindAny exposes its payload through Any to avoid double accessors.
	if v.kind != KindAny {
		//: typed Kinds expose their payload via the typed accessor.
		return nil
	}
	//: direct read of the opaque payload.
	return v.any
}
