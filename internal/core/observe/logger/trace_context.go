package logger

import (
	"encoding/hex"
)

// TraceIDLen and SpanIDLen are the byte lengths W3C Trace Context fixes for
// the two identifiers: 16 bytes of trace-id, 8 of span-id. They are not
// tunable — they are the format, not a default.
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

// TraceIDKey and SpanIDKey are the field names OpenTelemetry prescribes for
// trace context in NON-OTLP log formats — "use the field names trace_id for
// the TraceId, span_id for the SpanId"
// (specification/compatibility/logging_trace_context.md). They are top-level
// keys of the record, not attributes, which is why they are rendered by the
// encoders rather than appended to RecordEvent.Attrs.
const (
	// TraceIDKey is the top-level field name carrying the trace identifier.
	TraceIDKey string = "trace_id"
	// SpanIDKey is the top-level field name carrying the span identifier.
	SpanIDKey string = "span_id"
)

// IsValid reports whether the pair names a span a backend can join: both
// identifiers present and non-zero.
//
// Both are required rather than either, and that follows the data model rather
// than convenience: "if a SpanId is provided, the corresponding TraceId should
// also be included" (specification/logs/data-model.md, Trace Context Fields).
// The only producer in this SDK is a W3C span context, which is itself invalid
// unless both identifiers are set, so a half-populated pair never reaches an
// encoder through a supported path — and if one is forged by hand, emitting
// half of it would produce a field no query can join on.
func (c TraceContextValue) IsValid() bool {
	//: the zero identifiers are what the two rules compare against; declared
	//: rather than written inline so the comparison reads as "is it unset".
	var unsetTrace [TraceIDLen]byte
	var unsetSpan [SpanIDLen]byte
	//: the two normative validity rules, and nothing else.
	return c.TraceID != unsetTrace && c.SpanID != unsetSpan
}

// appendTraceIDHex is TraceContextValue.AppendTraceIDHex's body: decl_gen.go writes TraceContextValue.AppendTraceIDHex, from the
// design, as one call of it.
func (c TraceContextValue) appendTraceIDHex(dst []byte) []byte {
	//: encode through a stack array — hex.Encode does not retain src or dst,
	//: so nothing here escapes and the append is the only buffer growth.
	var buf [TraceIDHexLen]byte
	hex.Encode(buf[:], c.TraceID[:])
	//: hand back the extended buffer.
	return append(dst, buf[:]...)
}

// appendSpanIDHex is TraceContextValue.AppendSpanIDHex's body: decl_gen.go writes TraceContextValue.AppendSpanIDHex, from the
// design, as one call of it.
func (c TraceContextValue) appendSpanIDHex(dst []byte) []byte {
	//: same stack-array encode; 8 bytes in, 16 hex digits out.
	var buf [SpanIDHexLen]byte
	hex.Encode(buf[:], c.SpanID[:])
	//: hand back the extended buffer.
	return append(dst, buf[:]...)
}
