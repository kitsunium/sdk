// Package trace — the span an unsampled trace gets.
package trace

import (
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"

	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
)

// noopSpan is the core/observe/trace.Span an unsampled span becomes.
//
// It still CARRIES a context, and that is the whole reason it is not nil. Three
// things depend on it:
//
//   - Inject writes the traceparent with the sampled bit CLEAR, so the next
//     service inherits the decision instead of taking a second, contradictory
//     one. A nil span would propagate nothing and every downstream hop would
//     start its own root — turning one dropped trace into N kept ones, which is
//     the opposite of sampling.
//   - A caller can log the trace id of a request that was not sampled, which is
//     how a support ticket gets correlated with a trace that was never stored.
//   - Nothing has to nil-check a Span. Returning nil from Start would put
//     `if span != nil` at every instrumentation site, and the one place it was
//     forgotten would panic in production on the cheap path.
//
// It is a value type with no pointer receiver, so it allocates nothing: an
// unsampled span costs one interface conversion and no heap.
type noopSpan struct {
	// context is the identity — valid and unsampled, minted as every span's
	// is (minting cannot fail).
	context coretrace.SpanContextValue
}

// SpanContext implements core/observe/trace.Span.
func (s noopSpan) SpanContext() coretrace.SpanContextValue {
	//: the identity that still propagates.
	return s.context
}

// SetAttrs implements core/observe/trace.Span and records nothing.
func (s noopSpan) SetAttrs(_ ...coreotel.AttrValue) {
	//: deliberately dropped — the span is not recorded.
}

// AddEvent implements core/observe/trace.Span and records nothing.
func (s noopSpan) AddEvent(_ string, _ ...coreotel.AttrValue) {
	//: deliberately dropped — the span is not recorded.
}

// SetStatus implements core/observe/trace.Span and records nothing.
func (s noopSpan) SetStatus(_ coretrace.StatusCode, _ string) {
	//: deliberately dropped — the span is not recorded.
}

// End implements core/observe/trace.Span and ships nothing.
func (s noopSpan) End() {
	//: there is no value to hand to a sink.
}
