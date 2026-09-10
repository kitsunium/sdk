// Package logger — declares the trace-correlation half of a log record: the
// TraceContextValue a RecordEvent carries, and the TraceContextSource port
// that reads one off a context.Context.
//
// The pair lives here rather than in internal/core/trace because a log record
// is not a span: it borrows two identifiers from one. Keeping the value local
// keeps this package stdlib-only — importing the trace domain would put its
// whole model (and core/metrics behind it) in front of every consumer that
// only wants a line on stderr. The binding between the two lives at the top
// layer, in pkg/v1/logger, which is allowed to know both domains. See
// ADR 0062.
package logger

import (
	"context"
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

// TraceContextValue is the trace correlation a single log record carries: the
// identity of the span the record was emitted inside.
//
// It is a VALUE and carries no flags and no tracestate, because a log line
// answers exactly one question — "which span produced this?" — and the
// sampling decision that governs the trace is not a property of the line.
//
// The zero value is the INVALID context, deliberately: an all-zero trace-id is
// what W3C Trace Context §3.2.2.3 declares invalid, and an all-zero span-id is
// §3.2.2.4's. So "no trace here" and "a trace nobody may join" are one value
// with one spelling, and every consumer has exactly one check to make.
type TraceContextValue struct {
	// TraceID identifies the whole trace; rendered as 32 lowercase hex digits.
	TraceID [TraceIDLen]byte
	// SpanID identifies the span within the trace; 16 lowercase hex digits.
	SpanID [SpanIDLen]byte
}

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

// AppendTraceIDHex appends the trace identifier to dst as 32 LOWERCASE hex
// digits and returns the extended buffer.
//
// The spelling is normative — "Trace IDs and span IDs must be lowercase and
// hex-encoded" (specification/compatibility/logging_trace_context.md) — and
// three formatters (the text encoder, the JSON encoder and the legacy text
// handler) have to agree on it byte for byte, which is why it lives beside the
// value rather than three times over in internal/service/logger.
//
// It appends and never returns a string: a string would be one heap allocation
// per emitted record, and the logger's one-allocation-per-emit contract has no
// room for a second. See ADR 0062 and pkg/v1/logger/BENCH.md.
func (c TraceContextValue) AppendTraceIDHex(dst []byte) []byte {
	//: encode through a stack array — hex.Encode does not retain src or dst,
	//: so nothing here escapes and the append is the only buffer growth.
	var buf [TraceIDHexLen]byte
	hex.Encode(buf[:], c.TraceID[:])
	//: hand back the extended buffer.
	return append(dst, buf[:]...)
}

// AppendSpanIDHex appends the span identifier to dst as 16 lowercase hex
// digits and returns the extended buffer. Same contract as AppendTraceIDHex.
func (c TraceContextValue) AppendSpanIDHex(dst []byte) []byte {
	//: same stack-array encode; 8 bytes in, 16 hex digits out.
	var buf [SpanIDHexLen]byte
	hex.Encode(buf[:], c.SpanID[:])
	//: hand back the extended buffer.
	return append(dst, buf[:]...)
}

// TraceContextSource reads the trace context carried by ctx.
//
// It is a FUNC port, so ADR 0039 is satisfied structurally: a func type cannot
// grow a method, so publishing it can never break a downstream implementation.
// It returns the zero (invalid) value rather than an error or an ok flag —
// running outside a trace is the normal state of most code, not a fault, and
// IsValid is the single question every caller already has to ask.
//
// Implementations MUST NOT allocate: they run once per emitted record.
type TraceContextSource func(ctx context.Context) (trace TraceContextValue)
