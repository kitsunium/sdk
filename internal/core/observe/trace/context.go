package trace

import "context"

// contextKeyType is the unexported key type for the context value. Unexported so
// nothing outside this package can write the slot, which is what keeps
// SpanContextFromContext's answer trustworthy: the only writers are
// ContextWithSpanContext, which stores a typed value, and ContextWithSpan, which
// stores the Span whose SpanContext is read back.
type contextKeyType struct{}

// spanContextKey is the singleton key. A struct{} key allocates nothing.
var spanContextKey contextKeyType

// ContextWithSpanContext returns a copy of parent carrying context.
//
// An INVALID context is still stored rather than skipped. Storing it is what
// makes "this scope deliberately has no trace" expressible: a handler that ran
// under an unsampled or absent traceparent shadows any outer context instead of
// silently re-parenting its children onto it.
func ContextWithSpanContext(parent context.Context, context SpanContextValue) context.Context {
	//: one typed value in one unexported slot.
	return contextWithValue(parent, context)
}

// contextWithValue is the single WithValue call site of a span context, kept
// apart so the shadowed parameter name above never reaches context.WithValue.
func contextWithValue(parent context.Context, value SpanContextValue) context.Context {
	//: the key type is unexported, so no other package can collide with it.
	return context.WithValue(parent, spanContextKey, value)
}

// ContextWithSpan returns a copy of parent carrying span's context, exactly as
// ContextWithSpanContext(parent, span.SpanContext()) would — SpanContextFromContext
// reads the same value back — without copying that value onto the heap.
//
// The context stores the span itself and asks it for its context when the
// context is read. A span the tracer returns is a pointer, so storing it costs
// no allocation, where boxing the span context does: an unsampled Start pays
// two allocations instead of three. The price is the contract it states: span's
// SpanContext must return the same value for as long as the context lives,
// which every span Start returns guarantees — its identity is immutable once
// minted. A nil span reads back as the invalid zero context, and shadows any
// outer one as ContextWithSpanContext's invalid context does.
func ContextWithSpan(parent context.Context, span Span) context.Context {
	//: the span, in the same unexported slot; a pointer-shaped span is not
	//: boxed, and a nil one is the stored absence SpanContextFromContext reads
	//: as the zero value.
	return context.WithValue(parent, spanContextKey, span)
}

// SpanContextFromContext returns the span context carried by ctx, or the invalid
// zero value when there is none.
//
// It never returns an error and never panics on a nil-ish context value: an
// absent trace is the normal state of most code, not a fault.
func SpanContextFromContext(ctx context.Context) SpanContextValue {
	//: a nil context has nothing to read; guarding it here means callers do
	//: not have to, on a path that runs per request.
	if ctx == nil {
		//: the invalid zero value.
		return SpanContextValue{}
	}
	//: the two writers store a SpanContextValue or a Span, nothing else.
	switch stored := ctx.Value(spanContextKey).(type) {
	//: ContextWithSpanContext's value: the recorded identity.
	case SpanContextValue:
		//: as stored.
		return stored
	//: ContextWithSpan's span: its immutable identity, read now.
	case Span:
		//: the span's own context.
		return stored.SpanContext()
	//: absence path.
	default:
		//: no trace in this scope.
		return SpanContextValue{}
	}
}
