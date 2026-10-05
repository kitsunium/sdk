// Package otlp — common.v1.AnyValue, restricted to the cases the shared
// attribute model has.
//
// Package otlp — the OTLP/HTTP sender's configuration, which each signal
// publishes under its own name.
//
// Package otlp — common.v1.KeyValue, the repeated message every OTLP payload
// spells its dimensions with, and its rendering from the shared attribute
// model.
//
// Package otlp — one OTLP/JSON document, and the timestamp every message in it
// carries.
//
// Package otlp is the OTLP machinery the metrics and trace signals share: the
// proto3-JSON scalar encodings and the common/resource messages every OTLP
// payload carries, the single-document marshal, the newline-terminated stream
// a writer-bound exporter emits, and the OTLP/HTTP sender — endpoint
// refusal, the default client and its own connection pool, the bounded body
// reads, and the classification of a collector's answer into three verdicts.
//
// It was written twice, once per signal (ADR 0048 for metrics, ADR 0051 for
// trace), and the two copies differed in nothing but the error codes, the
// wording of their refusals and the name of the field a partial success counts.
// Those three are exactly what this package does NOT own: a signal hands it a
// SignalSpec value carrying its sentinels, its wrap parameters and its field names,
// so every error leaves here under the calling signal's dotted-quad code and in
// its words, byte for byte what that signal returned before the transport was
// shared. This package declares no code.
//
// What stays in each signal is what is genuinely its own: the payload tree
// (ResourceMetrics or ResourceSpans and everything below them), the encoder
// that builds it and the refusals only that encoder can raise, the signal's
// path constant, and the exported names a caller types — OTLPHTTPConfig,
// NewOTLPHTTPExporter, OTLPRetryable.
//
// It is internal to internal/service on purpose: nothing above the service
// layer may reach it, and nothing but the two signals needs to.
//
// Package otlp — resource.v1.Resource, carried once per payload by every
// signal.
//
// Package otlp — the collector's answer to an OTLP/HTTP export, and the
// lenient 64-bit decoder the specification asks for.
//
// Package otlp — the three proto3-JSON scalar encodings OTLP inherits and
// encoding/json does not produce on its own.
//
// Package otlp — common.v1.InstrumentationScope, carried once per payload by
// every signal.
//
// Package otlp — the OTLP/HTTP sender: the only file of this package, and of
// the two signals that use it, that opens an outbound socket.
//
// Package otlp — SignalSpec: everything the shared OTLP/HTTP transport needs to
// know about the signal it is carrying, and nothing it owns itself.
//
// Package otlp — the newline-delimited stream a writer-bound OTLP/JSON
// exporter emits.
package otlp
