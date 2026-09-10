// Package trace — the four facts that identify a span on the wire.
package trace

// SpanContextValue is the immutable identity of a span: what traceparent and
// tracestate carry between two processes, and what a child span inherits.
//
// It is a VALUE, not a handle. A span context can be copied, compared field by
// field and stored on a context.Context without any of it referring back to a
// live span — which is what makes propagation across a process boundary the
// same operation as propagation across a function call.
//
// The zero value is the INVALID context, and that is deliberate rather than
// incidental: an all-zero TraceID is exactly what W3C Trace Context §3.2.2.3
// declares invalid, so "no trace here" and "a trace nobody may join" are one
// value with one spelling. Every consumer therefore has exactly one check to
// make — IsValid — instead of a nil test and a validity test that can disagree.
type SpanContextValue struct {
	// TraceID identifies the whole trace. Shared by every span in it.
	TraceID TraceID
	// SpanID identifies this span within the trace.
	SpanID SpanID
	// Flags is the traceparent flag byte, kept EXACTLY as received. Undefined
	// bits are masked at format time (TraceFlags.Sanitized), never on receipt.
	Flags TraceFlags
	// State is the tracestate list travelling beside the traceparent. It is
	// propagated verbatim; this SDK adds no entry of its own.
	State StateValue
	// Remote reports whether this context was EXTRACTED from a carrier rather
	// than created in this process. OTLP carries the fact as a span flag, and
	// a backend needs it to tell "my caller" from "my own parent span".
	Remote bool
}

// IsValid reports whether the context names a joinable span: both identifiers
// present and non-zero.
func (c SpanContextValue) IsValid() bool {
	//: the two normative validity rules, and nothing else — flags and state
	//: are optional decoration on a context that is already valid or not.
	return c.TraceID.IsValid() && c.SpanID.IsValid()
}

// IsSampled reports whether the sampled bit is set on this context.
//
// It is the ONLY sampling question anything downstream asks. The decision is
// taken once, at the root of the trace, and travels in this bit; re-deciding per
// span produces a trace with holes in the middle, which is worse than no trace
// at all because it looks complete.
func (c SpanContextValue) IsSampled() bool {
	//: bit 0 of the traceparent flag byte.
	return c.Flags.IsSampled()
}

// WithState returns a copy of c carrying state. It exists so a caller who
// mutates the vendor list does not have to rebuild the identity around it.
func (c SpanContextValue) WithState(state StateValue) SpanContextValue {
	//: value receiver: c is already a copy, so this mutates nothing.
	c.State = state
	//: the updated identity.
	return c
}
