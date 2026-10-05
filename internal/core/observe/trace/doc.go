// Package trace — this signal's half of the shared attribute model
// (internal/core/observe/otel): the refusal an unusable set earns HERE, and the
// Resource a Tracer publishes.
//
// Package trace — the port a span context crosses a process boundary through.
// Reading and writing the W3C headers through it — Inject, Extract — is the
// engine's mechanism, internal/service/observe/trace (ADR 0160 §4).
//
// Package trace — range 0.2.20.* (ADR 0051 core/observe/trace block), and
// range 0.3.50.*, the tracing engine's, declared here since ADR 0160.
//
// Package trace — carrying a span context on a context.Context.
//
// Package trace — declares the sentinel *errs.Error values. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// Package trace — Event: a timestamped point inside a span.
//
// Package trace — the SpanExporter contract + process-wide exporter registry.
//
// Package trace — the two identifiers a trace is built out of, and the flag
// byte that travels with them.
//
// Package trace — Link: a reference to a span in another trace.
//
// Package trace — the sampling port.
//
// Package trace — what a Sampler is given to decide with.
//
// Package trace — the instrumentation scope this domain publishes.
//
// Package trace — the four facts that identify a span on the wire.
//
// Package trace — SpanKind: the span's relationship to its neighbours.
//
// Package trace — SpanValue: one finished span, the unit an exporter ships.
//
// Package trace — SpansValue: the exportable payload.
//
// Package trace — StateBuilder: a tracestate list assembled member by member,
// under the same checks Insert applies.
//
// Package trace — tracestate: the W3C vendor list that travels beside a
// traceparent, as a value. Reading it out of a header is the engine's
// (internal/service/observe/trace, ADR 0160 §4).
//
// Package trace — Status: whether the operation the span describes succeeded.
//
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
// OTLP/JSON exporter — live in internal/service/observe/trace. See ADR 0051.
//
// Package trace — one list member of a tracestate header.
package trace
