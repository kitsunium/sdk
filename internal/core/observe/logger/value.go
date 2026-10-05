package logger

import (
	"math"
	"time"
)

// boolOne is the uint64 mask representing the boolean value true; zero
// represents false. Stored in bits via the pack/unpack helpers.
const boolOne packedBits = 1

// packedBits is the named uint64 alias backing the bit-packed Kinds; the
// typed accessors (Uint64 / Int64 / Float64 / …) read it under different
// reinterpretations rather than each owning its own field.
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
func NewValue(v any) Value {
	//: single source of truth lives in AnyValue.
	return AnyValue(v)
}

// StringValue builds a Value of kind string.
func StringValue(v string) Value {
	//: store the payload in the string field; bits is unused for this kind.
	return Value{kind: KindString, str: v}
}

// int64Value is Int64Value's body: decl_gen.go writes Int64Value, from the
// design, as one call of it.
func int64Value(v int64) Value {
	//: two's complement reinterpretation lets num hold any int64 without loss.
	return Value{kind: KindInt64, bits: packedBits(uint64(v))}
}

// intValue is IntValue's body: decl_gen.go writes IntValue, from the
// design, as one call of it.
func intValue(v int) Value {
	//: delegate to Int64Value so the encoding contract has a single source.
	return Int64Value(int64(v))
}

// uint64Value is Uint64Value's body: decl_gen.go writes Uint64Value, from the
// design, as one call of it.
func uint64Value(v uint64) Value {
	//: direct copy — uint64 already fits the bits field after alias conversion.
	return Value{kind: KindUint64, bits: packedBits(v)}
}

// float64Value is Float64Value's body: decl_gen.go writes Float64Value, from the
// design, as one call of it.
func float64Value(v float64) Value {
	//: bit-cast preserves NaN/Inf round-trip semantics.
	return Value{kind: KindFloat64, bits: packedBits(math.Float64bits(v))}
}

// boolValue is BoolValue's body: decl_gen.go writes BoolValue, from the
// design, as one call of it.
func boolValue(v bool) Value {
	//: avoid a branch per conversion via bool→uint64 lookup.
	if v {
		//: true encodes as boolOne so handlers can compare against the constant.
		return Value{kind: KindBool, bits: boolOne}
	}
	//: false encodes as 0 — the natural zero value.
	return Value{kind: KindBool, bits: 0}
}

// durationValue is DurationValue's body: decl_gen.go writes DurationValue, from the
// design, as one call of it.
func durationValue(v time.Duration) Value {
	//: delegate the double-cast to durationBits so the intent is named.
	return Value{kind: KindDuration, bits: packedBits(durationBits(v))}
}

// durationBits reinterprets a time.Duration as the uint64 bit pattern used
// to back Value.bits. Isolates the mandatory two-step conversion
// (time.Duration → int64 → uint64) to one named helper so call sites stop
// carrying the chained cast and the "why two casts?" comment lives here.
func durationBits(d time.Duration) uint64 {
	//: time.Duration is int64 under the hood; the int64 trip is required
	//: because Go's conversion rules do NOT permit named-type-to-unsigned
	//: without the intermediate unnamed-type step. Two's-complement layout
	//: guarantees the Int64()/Duration() roundtrip is lossless.
	return uint64(int64(d))
}

// timeValue is TimeValue's body: decl_gen.go writes TimeValue, from the
// design, as one call of it.
func timeValue(v time.Time) Value {
	//: keep the time payload boxed for now; allocation-free packing is future work.
	return Value{kind: KindTime, any: v}
}

// groupValue is GroupValue's body: decl_gen.go writes GroupValue, from the
// design, as one call of it.
func groupValue(attrs ...AttrValue) Value {
	//: store the slice verbatim — handlers iterate it as needed.
	return Value{kind: KindGroup, any: attrs}
}

// anyValue is AnyValue's body: decl_gen.go writes AnyValue, from the
// design, as one call of it.
func anyValue(v any) Value {
	//: store the opaque payload verbatim so handlers can type-switch on it.
	return Value{kind: KindAny, any: v}
}

// Kind returns the discriminator for this Value, indicating which typed
// accessor (String / Int64 / Float64 / …) handlers should call.
func (v Value) Kind() Kind {
	//: direct read — the discriminator is part of the API contract.
	return v.kind
}

// String returns the textual payload when the Value carries KindString; for
// any other Kind it returns an empty string. Handlers that need a stringified
// rendering of non-string Kinds MUST format from the typed accessor instead.
func (v Value) String() string {
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
func (v Value) Int64() int64 {
	//: reverse the two's complement reinterpretation done by Int64Value.
	return int64(uint64(v.bits))
}

// Bits returns the raw uint64 storage that backs all bit-packed Kinds
// (KindBool / KindInt64 / KindUint64 / KindFloat64 / KindDuration). Callers
// SHOULD prefer the typed accessors; Bits exists for tooling and tests that
// need direct access to the packed storage.
func (v Value) Bits() packedBits {
	//: direct read of the packed storage; meaningful only with Kind context.
	return v.bits
}

// Uint64 returns the uint64 payload. The result is undefined when Kind is not
// KindUint64; callers MUST guard with Kind() before calling this accessor.
func (v Value) Uint64() uint64 {
	//: cast back to uint64 — bits is the packedBits alias underneath.
	return uint64(v.bits)
}

// Float64 returns the float64 payload. The result is undefined when Kind is
// not KindFloat64; callers MUST guard with Kind() before calling this
// accessor.
func (v Value) Float64() float64 {
	//: undo the bit-cast performed by Float64Value.
	return math.Float64frombits(uint64(v.bits))
}

// Bool returns the boolean payload. The result is undefined when Kind is not
// KindBool; callers MUST guard with Kind() before calling this accessor.
func (v Value) Bool() bool {
	//: any non-zero bits decodes as true (BoolValue stores boolOne for true).
	return v.bits != 0
}

// duration is Value.Duration's body: decl_gen.go writes Value.Duration, from the
// design, as one call of it.
func (v Value) duration() time.Duration {
	//: reverse the two's complement reinterpretation done by DurationValue.
	return time.Duration(int64(uint64(v.bits)))
}

// Time returns the time.Time payload. The result is undefined when Kind is
// not KindTime; callers MUST guard with Kind() before calling this accessor.
// A nil any field decodes as the zero time.Time.
func (v Value) Time() time.Time {
	//: comma-ok defends against the (impossible-by-contract) wrong any payload.
	parsed, ok := v.any.(time.Time)
	//: guard so callers never observe a non-Time value of any kind.
	if !ok {
		//: zero time is the documented degraded result.
		return time.Time{}
	}
	//: hand back the stored timestamp.
	return parsed
}

// Group returns the nested AttrValue slice. The result is undefined when
// Kind is not KindGroup; callers MUST guard with Kind() before calling this
// accessor. A nil any field decodes as a nil slice.
func (v Value) Group() []AttrValue {
	//: comma-ok defends against the (impossible-by-contract) wrong any payload.
	attrs, ok := v.any.([]AttrValue)
	//: guard so callers never observe a non-slice payload of any kind.
	if !ok {
		//: nil slice is the documented degraded result.
		return nil
	}
	//: hand back the stored attribute list.
	return attrs
}

// Any returns the opaque payload for KindAny. For typed Kinds the return is
// nil — callers should call the typed accessor instead.
func (v Value) Any() any {
	//: only KindAny exposes its payload through Any to avoid double accessors.
	if v.kind != KindAny {
		//: typed Kinds expose their payload via the typed accessor.
		return nil
	}
	//: direct read of the opaque payload.
	return v.any
}
