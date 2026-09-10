// Package trace — Link: a reference to a span in another trace.
package trace

import coremetrics "github.com/kitsunium/sdk/internal/core/metrics"

// LinkValue points at a span that is causally related to this one but is not its
// parent — `opentelemetry/proto/trace/v1.Span.Link`.
//
// The canonical case is a batch: one consumer span processes fifty messages
// produced by fifty different traces. Making any one of them the parent would be
// a lie about forty-nine, and making the batch a child of all fifty is not a
// tree. A link says "related, elsewhere" without claiming ancestry.
//
// It carries a whole SpanContextValue rather than a loose trace-id/span-id pair
// because the linked context's tracestate and sampled bit are part of what a
// backend needs: a link into a trace that was never sampled points at spans
// nobody stored.
type LinkValue struct {
	// Context identifies the linked span. A link whose context is invalid is
	// dropped rather than emitted — it names nothing.
	Context SpanContextValue
	// Attrs describe the relationship, sorted by Key.
	Attrs []coremetrics.AttrValue
}

// Normalized returns the link a span actually records: attributes sorted,
// validated and owned.
func (l LinkValue) Normalized() LinkValue {
	//: SortAttrs panics on an unusable set, at the call site that wrote it.
	return LinkValue{Context: l.Context, Attrs: coremetrics.SortAttrs(l.Attrs)}
}

// IsValid reports whether the link names a joinable span.
func (l LinkValue) IsValid() bool {
	//: a link is exactly as valid as the context it carries.
	return l.Context.IsValid()
}
