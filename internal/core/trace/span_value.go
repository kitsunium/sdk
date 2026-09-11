// Package trace — SpanValue: one finished span, the unit an exporter ships.
package trace

import (
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
)

// SpanValue is a completed span: the OpenTelemetry trace data model's `Span`
// message, in Go.
//
// It is IMMUTABLE and it is what a live Span becomes at End. Splitting the two
// is what keeps an exporter from ever seeing a span mid-flight: the recording
// type has mutators and no exporter can reach it, this one has no mutators and
// is all an exporter is given.
//
// Two fields are whole SpanContextValues rather than loose identifiers, and both
// pay for themselves:
//
//   - Context carries the trace-id, the span-id, the sampled bit and the
//     tracestate as ONE value — the same value that travelled in the
//     traceparent, so nothing has to be reassembled to know what was propagated.
//   - Parent carries its own Remote flag, which is the only place the fact is
//     recorded that this span's parent ran in another process. OTLP has a field
//     for exactly that (`flags`, SPAN_FLAGS_CONTEXT_IS_REMOTE_MASK), and a
//     backend uses it to tell a service boundary from an internal call.
//
// A root span's Parent is the invalid zero value, which OTLP encodes as an
// omitted parentSpanId — the schema's own spelling for "no parent".
type SpanValue struct {
	// Context identifies this span: trace-id, span-id, flags, tracestate.
	Context SpanContextValue
	// Parent identifies the span this one descends from. The zero value means
	// this is a root span.
	Parent SpanContextValue
	// Name is the low-cardinality operation label a backend groups on.
	Name string
	// Kind is the span's relationship to its neighbours, already resolved —
	// never SpanKindUnspecified on a span a Tracer produced.
	Kind SpanKind
	// StartTime opens the interval the span covers.
	StartTime time.Time
	// EndTime closes it. A span whose EndTime is unset never ended, and no
	// exporter in this SDK will ship one.
	EndTime time.Time
	// Attrs are the span's typed dimensions, sorted by Key.
	Attrs []coremetrics.AttrValue
	// Events are timestamped points inside the interval, in the order they
	// were recorded.
	Events []EventValue
	// Links point at causally related spans in other traces.
	Links []LinkValue
	// Status is the recorded outcome, already resolved.
	Status StatusValue
}

// Duration reports how long the span covered, or zero when it never ended.
func (s SpanValue) Duration() time.Duration {
	//: a span with no end has no duration to report — not a negative one.
	if s.StartTime.IsZero() || s.EndTime.IsZero() || s.EndTime.Before(s.StartTime) {
		//: zero, which is what "unknown" is spelled as here.
		return 0
	}
	//: the closed interval.
	return s.EndTime.Sub(s.StartTime)
}

// IsRoot reports whether the span has no parent.
func (s SpanValue) IsRoot() bool {
	//: an invalid parent context is the schema's "no parent".
	return !s.Parent.IsValid()
}
