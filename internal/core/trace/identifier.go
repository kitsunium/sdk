// Package trace — the two identifiers a trace is built out of, and the flag
// byte that travels with them.
package trace

import (
	"encoding/hex"
	"slices"
)

// TraceIDLen and SpanIDLen are the byte lengths W3C Trace Context and
// opentelemetry/proto/trace/v1 both fix: 16 bytes of trace-id, 8 of span-id.
// They are not tunable — a 16-byte trace-id is the format, not a default.
const (
	// TraceIDLen is the trace identifier's length in bytes.
	TraceIDLen int = 16
	// SpanIDLen is the span identifier's length in bytes.
	SpanIDLen int = 8
	// TraceIDHexLen is the trace identifier's length in lowercase hex.
	TraceIDHexLen int = TraceIDLen * 2
	// SpanIDHexLen is the span identifier's length in lowercase hex.
	SpanIDHexLen int = SpanIDLen * 2
)

// TraceID identifies one trace: 16 bytes, rendered as 32 lowercase hex digits
// on the wire.
//
// The ALL-ZERO value is invalid, and that is normative rather than stylistic —
// W3C Trace Context §3.2.2.3 says a receiver "MUST ignore the traceparent" when
// the trace-id is all zeroes, and the OTLP schema documents the same. It is
// therefore the zero value of this type AND its "unset" spelling, which is why
// IsValid exists and why nothing in this package accepts an id without asking.
type TraceID [TraceIDLen]byte

// SpanID identifies one span within a trace: 8 bytes, 16 lowercase hex digits.
// The all-zero value is invalid for the same normative reason TraceID's is
// (§3.2.2.4), and is additionally how "this span has no parent" is spelled —
// a root span's ParentSpanID is the zero value, which OTLP encodes as an
// omitted parentSpanId.
type SpanID [SpanIDLen]byte

// TraceFlags is the 8-bit field traceparent carries as its last component.
//
// Version 00 of the specification defines exactly one bit, and §3.2.2.5.2 is
// explicit about the rest: "The behavior of other flags … is not defined and is
// reserved for future use. Vendors MUST set those to zero." This SDK therefore
// MASKS on output rather than trusting what it was handed — see Sanitized.
type TraceFlags byte

// FlagSampled is the sampled bit: the least significant one. When set, the
// caller "may have recorded trace data" (§3.2.2.5.1), which is what makes the
// sampling decision a property of the TRACE rather than of each span.
const FlagSampled TraceFlags = 0x01

// IsValid reports whether t is a usable trace identifier, i.e. not all zeroes.
func (t TraceID) IsValid() bool {
	//: the all-zero id is the specification's own invalid value.
	return t != TraceID{}
}

// String renders the identifier as 32 lowercase hex digits — the spelling both
// traceparent and OTLP/JSON use. OTLP/JSON is the one place the protobuf-JSON
// mapping is overridden: "the traceId and spanId byte arrays are represented as
// case-insensitive hex-encoded strings; they are not base64-encoded".
func (t TraceID) String() string {
	//: lowercase hex is the only form the traceparent grammar accepts.
	return hex.EncodeToString(t[:])
}

// ParseTraceID reads 32 lowercase hex digits into a TraceID, refusing an
// all-zero result.
//
// Uppercase is refused rather than folded: the traceparent grammar is 32HEXDIGLC
// and a producer emitting uppercase is producing a header other vendors will
// reject, so accepting it here would hide the defect exactly one hop from where
// it was introduced.
func ParseTraceID(text string) (id TraceID, err error) {
	//: length first — a short string cannot be a trace-id and needs no decode.
	if len(text) != TraceIDHexLen || !isLowerHex(text) {
		//: the header is attacker-controlled, so nothing of it is echoed.
		return TraceID{}, InvalidTraceParent
	}
	//: decode into the fixed array; the length check above makes this exact.
	var decoded TraceID
	//: hex.Decode cannot fail after isLowerHex, but the error is not ignored.
	if _, decodeErr := hex.Decode(decoded[:], []byte(text)); decodeErr != nil {
		//: same refusal — an undecodable id is an invalid traceparent.
		return TraceID{}, InvalidTraceParent
	}
	//: all zeroes is a syntactically valid id the specification forbids.
	if !decoded.IsValid() {
		//: refuse, so a caller cannot start a trace nobody can join.
		return TraceID{}, InvalidTraceParent
	}
	//: a usable trace identifier.
	return decoded, nil
}

// IsValid reports whether s is a usable span identifier, i.e. not all zeroes.
func (s SpanID) IsValid() bool {
	//: the all-zero id is both "invalid" and "no parent".
	return s != SpanID{}
}

// String renders the identifier as 16 lowercase hex digits.
func (s SpanID) String() string {
	//: lowercase hex, for the same reason TraceID.String uses it.
	return hex.EncodeToString(s[:])
}

// ParseSpanID reads 16 lowercase hex digits into a SpanID, refusing an all-zero
// result for the reason ParseTraceID does.
func ParseSpanID(text string) (id SpanID, err error) {
	//: length + alphabet before any decode.
	if len(text) != SpanIDHexLen || !isLowerHex(text) {
		//: nothing of the header is echoed.
		return SpanID{}, InvalidTraceParent
	}
	//: decode into the fixed array.
	var decoded SpanID
	//: unreachable after isLowerHex, and still not ignored.
	if _, decodeErr := hex.Decode(decoded[:], []byte(text)); decodeErr != nil {
		//: same refusal.
		return SpanID{}, InvalidTraceParent
	}
	//: all zeroes is the specification's invalid parent-id.
	if !decoded.IsValid() {
		//: refuse rather than propagate a parent nobody can resolve.
		return SpanID{}, InvalidTraceParent
	}
	//: a usable span identifier.
	return decoded, nil
}

// IsSampled reports whether the sampled bit is set.
func (f TraceFlags) IsSampled() bool {
	//: bit 0 is the only flag version 00 defines.
	return f&FlagSampled != 0
}

// WithSampled returns f with the sampled bit set to sampled, leaving every
// other bit as it was.
func (f TraceFlags) WithSampled(sampled bool) TraceFlags {
	//: setting is an or, clearing is an and-not; neither touches the rest.
	if sampled {
		//: raise bit 0.
		return f | FlagSampled
	}
	//: clear bit 0.
	return f &^ FlagSampled
}

// Sanitized returns f with every bit this specification version does not define
// cleared.
//
// It is applied on the way OUT, never on the way in. §3.2.2.5.2 says a vendor
// MUST set undefined bits to zero, so emitting a bit we did not set would make
// this SDK a non-conforming producer; but §3.2.4 says a receiver must not
// "assume anything about unknown fields", so clearing them on the way in would
// destroy information a future version defines. Keeping the received byte and
// masking at format time satisfies both halves at once.
func (f TraceFlags) Sanitized() TraceFlags {
	//: FlagSampled is the whole defined surface of version 00.
	return f & FlagSampled
}

// isLowerHex reports whether text is drawn entirely from [0-9a-f].
//
// hex.DecodeString accepts uppercase, and the traceparent grammar does not
// (32HEXDIGLC / 16HEXDIGLC), so the alphabet is checked here rather than left to
// the decoder.
func isLowerHex(text string) bool {
	//: no allocation: the check walks the bytes in place.
	return !slices.ContainsFunc([]byte(text), isNotLowerHexDigit)
}

// isNotLowerHexDigit reports whether c falls outside [0-9a-f].
func isNotLowerHexDigit(c byte) bool {
	//: digits and the six lowercase letters are the entire alphabet.
	return (c < '0' || c > '9') && (c < 'a' || c > 'f')
}
