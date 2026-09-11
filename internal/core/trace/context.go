// Package trace — carrying a span context on a context.Context.
package trace

import "context"

// contextKeyType is the unexported key type for the context value. Unexported so
// nothing outside this package can write the slot, which is what keeps
// SpanContextFromContext's answer trustworthy: the only writer is
// ContextWithSpanContext, which takes a typed value.
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

// contextWithValue is the single WithValue call site, kept apart so the shadowed
// parameter name above never reaches context.WithValue.
func contextWithValue(parent context.Context, value SpanContextValue) context.Context {
	//: the key type is unexported, so no other package can collide with it.
	return context.WithValue(parent, spanContextKey, value)
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
	//: the only writer stores a SpanContextValue, so the assertion is total.
	stored, ok := ctx.Value(spanContextKey).(SpanContextValue)
	//: absence path.
	if !ok {
		//: no trace in this scope.
		return SpanContextValue{}
	}
	//: the recorded identity.
	return stored
}
