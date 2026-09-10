// Package trace declares the SDK's distributed-tracing PORT: the span context
// that travels between processes, the OpenTelemetry trace data model, the W3C
// Trace Context propagation format, and the Tracer/Span pair an application
// instruments against.
//
// It is the third pillar of observability beside `logger` and `metrics`, and —
// like `metrics` since ADR 0044 — it speaks the OpenTelemetry data model while
// importing none of OpenTelemetry's code. OTel is a published specification;
// this SDK implements it from the document, as it implements the Prometheus
// exposition format, RFC 7517 and a five-field POSIX cron. What OTel buys is
// INTEROPERABILITY, and interoperability is a property of the wire, not of the
// import graph.
//
// Concrete implementations — the Tracer, the samplers, the recorder and the
// OTLP/JSON exporter — live in internal/service/trace. See ADR 0051.
package trace

import (
	"context"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
)

// Tracer starts spans. It is the only way a span comes into existence.
//
// IFACE-PLUGIN: it is FROZEN at one method (ADR 0039). Everything a caller might
// want to add — a second Start taking an explicit clock, a Flush, a Shutdown —
// is reachable as a sibling interface or as a method on the concrete type, and
// none of them is worth breaking every downstream double for.
//
// Start returns a context carrying the new span's context, so a callee that
// starts its own span becomes a child without either function naming the other.
// The Span is returned separately rather than fished back out of the context,
// because a caller who has to look up the span they just created will eventually
// look up the wrong one.
//
// An EMPTY name panics with InvalidSpanName: a span name is the low-cardinality
// operation label every backend groups on, it is a literal at the call site, and
// it is therefore wrong on the first call or never — the same argument that makes
// an unusable attribute key a panic in `metrics`.
type Tracer interface {
	Start(ctx context.Context, name string, params SpanParams) (child context.Context, span Span)
}

// Span is one live, recording span.
//
// IFACE-PLUGIN: FROZEN at five methods (ADR 0039). Two things a reader will
// expect and not find, each deliberate:
//
//   - There is no RecordError. It would have to decide what an error's TYPE is,
//     which is a judgement about the caller's error model, and it is expressible
//     as one AddEvent call. pkg/v1/trace ships it as a HELPER over this port
//     instead, which is exactly the shape that does not freeze a decision into a
//     port.
//   - End takes no timestamp. A span that ends at a time other than "now" is
//     either replaying history or working around a clock, and both want a sibling
//     interface rather than a parameter every caller has to pass a zero value to.
//
// Every method is safe for concurrent use and every one is a NO-OP on an
// unsampled span, so instrumentation costs nothing when sampling is off. End is
// idempotent: the second call is ignored rather than emitting the span twice.
type Span interface {
	// SpanContext returns the span's identity — what Inject propagates.
	SpanContext() SpanContextValue
	// SetAttrs adds or replaces typed dimensions on the span.
	SetAttrs(attrs ...coremetrics.AttrValue)
	// AddEvent records a timestamped point inside the span.
	AddEvent(name string, attrs ...coremetrics.AttrValue)
	// SetStatus records the operation's outcome.
	SetStatus(code StatusCode, message string)
	// End closes the span and hands it to the Tracer's SpanSink.
	End()
}
