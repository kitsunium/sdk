// Package trace — the facts a span is born with.
package trace

import (
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
)

// SpanParams are the facts a span is born with.
//
// It is a struct rather than a variadic option list because an option type is a
// published func the SDK would then have to freeze (ADR 0039), and because the
// zero value has to be usable: SpanParams{} is an INTERNAL span starting now
// with no attributes, which is the common case and reads as one at the call site.
type SpanParams struct {
	// Kind is the span's relationship to its neighbours. The zero value
	// resolves to SpanKindInternal (SpanKind.Resolved).
	Kind SpanKind
	// Attrs are the dimensions known at Start. They are the ONLY ones a
	// Sampler sees, so anything the sampling decision depends on belongs here
	// rather than in a later SetAttrs.
	Attrs []coremetrics.AttrValue
	// Links point at causally related spans in other traces.
	Links []LinkValue
	// StartTime overrides the span's start instant. The zero value means
	// "now", which is what every caller but a replaying one wants — and it is
	// a CLAMP rather than a refusal because "now" is a description of what the
	// tracer does, not a value chosen on the caller's behalf (ADR 0031).
	StartTime time.Time
}
