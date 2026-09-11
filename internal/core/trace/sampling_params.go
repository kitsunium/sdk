// Package trace — what a Sampler is given to decide with.
package trace

import coremetrics "github.com/kitsunium/sdk/internal/core/metrics"

// SamplingParams is everything a Sampler sees. It describes a span that does not
// exist yet, which is the point: the decision is taken before the span is
// created, so an unsampled span costs no recording at all.
//
// TraceID is present and Parent may not be. On a ROOT span the trace id has just
// been minted and there is no parent; on a CHILD it is the parent's, inherited
// unchanged. A ratio sampler reads it either way, which is what makes the same
// sampler usable at both ends without a special case.
type SamplingParams struct {
	// Parent is the context this span descends from, or the invalid zero
	// value when this is a root.
	Parent SpanContextValue
	// TraceID is the trace the new span belongs to.
	TraceID TraceID
	// Name is the span's operation name.
	Name string
	// Kind is the span's relationship to its neighbours.
	Kind SpanKind
	// Attrs are the attributes supplied at Start, sorted by Key. Attributes
	// added AFTER Start are invisible here — the decision has already been
	// taken by then, which is the price of taking it once.
	Attrs []coremetrics.AttrValue
	// Links are the links supplied at Start.
	Links []LinkValue
}
