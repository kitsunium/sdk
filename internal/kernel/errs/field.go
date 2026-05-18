// Package errs: field.go defines the closed FieldValue union used to
// attach structured metadata to an Error without opening an `any` back
// door. FieldValue is a transport + textual-restitution contract, NOT a
// vehicle for strongly-typed reconstruction on the consumer side —
// StringValue is the only value accessor exposed in this MR.
package errs

import "strconv"

// decimalBase is the radix used when rendering integer FieldValue payloads.
const decimalBase int = 10

// floatPrec is the shortest-round-trip sentinel for strconv.FormatFloat.
const floatPrec int = -1

// floatBitSize tells strconv.FormatFloat we are formatting a 64-bit float.
const floatBitSize int = 64

// floatFormat selects strconv's general exponent-or-decimal mode.
const floatFormat byte = 'g'

// fieldKind discriminates a FieldValue's underlying type. The concrete
// values are unexported so callers cannot construct a FieldValue by struct
// literal — only the String/Int/Bool/Float/NewFieldValue helpers produce
// well-formed instances.
type fieldKind uint8

// fieldInvalid is the zero-value tag; it marks a FieldValue that was not
// built through one of the constructors. StringValue degrades to "" for it.
const fieldInvalid fieldKind = 0

// fieldString tags a FieldValue whose payload lives in the private str member.
const fieldString fieldKind = 1

// fieldInt tags a FieldValue whose payload lives in the private num member.
const fieldInt fieldKind = 2

// fieldBool tags a FieldValue whose payload lives in the private bl member.
const fieldBool fieldKind = 3

// fieldFloat tags a FieldValue whose payload lives in the private fl member.
const fieldFloat fieldKind = 4

// FieldValue is an immutable typed key/value pair attached to an *Error.
// Use the exported constructors (String, Int, Bool, Float, NewFieldValue)
// to create a valid FieldValue — the zero value is invalid and must never
// be passed across the API.
type FieldValue struct {
	key  string
	kind fieldKind
	str  string
	num  int64
	bl   bool
	fl   float64
}

// NewFieldValue builds a string-typed FieldValue. Provided as a generic
// constructor for tooling that expects a New-prefixed factory; for common
// cases prefer the dedicated String/Int/Bool/Float helpers.
//
// Params:
//   - key: attribute identifier.
//   - val: string payload rendered verbatim by StringValue.
//
// Returns:
//   - FieldValue: a well-formed FieldValue of kind string.
func NewFieldValue(key, val string) FieldValue {
	//: delegate to String so the canonical path owns the invariant.
	return String(key, val)
}

// String builds a FieldValue holding a string value.
//
// Params:
//   - key: attribute identifier shown next to the value in logs.
//   - val: string payload rendered verbatim by StringValue.
//
// Returns:
//   - FieldValue: a well-formed FieldValue of kind string.
func String(key, val string) FieldValue {
	//: FieldValues are immutable after construction.
	return FieldValue{key: key, kind: fieldString, str: val}
}

// Int builds a FieldValue from a plain int. Matches the slog / zap
// convention (both expose Int(key, int) that widens internally) so
// callers do not write int64(…) at every site. For pre-widened int64
// payloads use Int64.
//
// Params:
//   - key: attribute identifier.
//   - val: int payload; widened to int64 internally for storage.
//
// Returns:
//   - FieldValue: a well-formed FieldValue of kind int.
func Int(key string, val int) FieldValue {
	//: widen to int64 so the underlying storage is width-stable.
	return FieldValue{key: key, kind: fieldInt, num: int64(val)}
}

// Int64 builds a FieldValue from a 64-bit integer. Provided explicitly
// because Go does not support function overloading — callers that hold
// an int64 already (no upcast needed) reach for this constructor, while
// the common int case stays on Int.
//
// Params:
//   - key: attribute identifier.
//   - val: int64 payload stored verbatim.
//
// Returns:
//   - FieldValue: a well-formed FieldValue of kind int.
func Int64(key string, val int64) FieldValue {
	//: store the caller-supplied int64 verbatim.
	return FieldValue{key: key, kind: fieldInt, num: val}
}

// Bool builds a FieldValue holding a boolean value.
//
// Params:
//   - key: attribute identifier.
//   - val: boolean payload rendered as "true" / "false".
//
// Returns:
//   - FieldValue: a well-formed FieldValue of kind bool.
func Bool(key string, val bool) FieldValue {
	//: bool has no base to configure — direct assignment.
	return FieldValue{key: key, kind: fieldBool, bl: val}
}

// Float builds a FieldValue holding a float64 value.
//
// Params:
//   - key: attribute identifier.
//   - val: float64 payload rendered with the shortest round-trip format.
//
// Returns:
//   - FieldValue: a well-formed FieldValue of kind float.
func Float(key string, val float64) FieldValue {
	//: 64-bit only — callers of float32 widen on call.
	return FieldValue{key: key, kind: fieldFloat, fl: val}
}

// Key returns the identifier under which this FieldValue was recorded.
//
// Returns:
//   - string: the key chosen at construction time.
func (f FieldValue) Key() string {
	//: direct read of the immutable struct member.
	return f.key
}

// StringValue returns a deterministic textual rendering of the payload
// suitable for log lines and error dumps. Not guaranteed to be machine-
// parseable back into the original type — by design the consumer contract
// stops at textual observation.
//
// Returns:
//   - string: stable rendering; empty string when the FieldValue is invalid.
func (f FieldValue) StringValue() string {
	//: dispatch on the private kind so unknown kinds degrade gracefully.
	switch f.kind {
	//: zero-value branch — Field was not built through a constructor.
	case fieldInvalid:
		//: degrade to empty so downstream code sees "no value" cleanly.
		return ""
	//: string payload — the most common case in practice.
	case fieldString:
		//: return the stored string verbatim.
		return f.str
	//: int64 payload — render decimal, unquoted.
	case fieldInt:
		//: format base-10 via strconv to avoid fmt.Sprintf in a hot path.
		return strconv.FormatInt(f.num, decimalBase)
	//: bool payload — small alphabet "true" / "false".
	case fieldBool:
		//: delegate to strconv for the canonical Go spelling.
		return strconv.FormatBool(f.bl)
	//: float payload — shortest round-trip form.
	case fieldFloat:
		//: general exponent-or-decimal mode with the documented precision.
		return strconv.FormatFloat(f.fl, floatFormat, floatPrec, floatBitSize)
	//: future-proof branch for kinds added after this file.
	default:
		//: degrade to empty rather than panic so logs stay forward-compatible.
		return ""
	}
}
