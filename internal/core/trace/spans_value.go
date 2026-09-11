// Package trace — SpansValue: the exportable payload.
package trace

import coremetrics "github.com/kitsunium/sdk/internal/core/metrics"

// SpansValue is a batch of finished spans plus the two facts that describe all
// of them at once — the OTLP payload hierarchy flattened into one Go value.
//
// It is the trace counterpart of metrics' SnapshotValue and it is shaped by the
// same rule (ADR 0044 §Decision 9): an OTLP encoder must be able to walk it
// WITHOUT regrouping or reconstruction.
//
//	SpansValue            -> resourceSpans[0]
//	  .Resource           ->   .resource.attributes
//	  .Scope              ->   .scopeSpans[0].scope {name, version}
//	  .Spans[i]           ->   .scopeSpans[0].spans[]
//
// The two single-element levels are single because one Tracer has exactly one
// Resource and one Scope, exactly as one Meter does.
//
// It carries NO StartTime/Time pair, and that absence is the model rather than
// an omission: a metrics snapshot is one window shared by every point, while
// every span carries its own interval. Copying a payload-level window down onto
// a span would overwrite the only timestamps a trace has.
type SpansValue struct {
	// Resource identifies the producer, carried once for the whole payload.
	Resource coremetrics.ResourceValue
	// Scope identifies the instrumentation, carried once for the whole
	// payload.
	Scope coremetrics.ScopeValue
	// Spans are the finished spans, in the order they ended. That order is
	// stable and is what makes an exporter's output diffable; it is NOT
	// causal — a parent ends after its children.
	Spans []SpanValue
}

// IsEmpty reports whether the payload carries no span. An exporter uses it to
// skip a POST that would carry nothing.
func (s SpansValue) IsEmpty() bool {
	//: resource and scope alone are not telemetry.
	return len(s.Spans) == 0
}
