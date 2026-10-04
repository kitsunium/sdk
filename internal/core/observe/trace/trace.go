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
package trace
