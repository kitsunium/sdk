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
package otlp

import "time"

// DefaultTimeout bounds one export round trip when an HTTPConfig leaves Timeout
// unset. Ten seconds is the value the OpenTelemetry protocol exporter
// specification itself defaults OTEL_EXPORTER_OTLP_TIMEOUT to, so it is a clamp
// onto the specification's own number rather than a figure this SDK invented
// (ADR 0031).
const DefaultTimeout time.Duration = 10 * time.Second

// DefaultMaxResponseBytes caps how much of a collector's response body is
// read: 1 MiB. A conforming response is an ExportMetricsServiceResponse, an
// ExportTraceServiceResponse or a Status message — hundreds of bytes. The cap
// exists because the body is the one length a REMOTE party controls in this
// exchange; the REQUEST body is deliberately not capped, because its size is a
// property of the caller's own volume, any SDK-chosen ceiling would be
// arbitrary (ADR 0031 §refuse), and the collector already answers 413 for one
// it will not take.
//
// It also bounds the drain that recycles the connection after the verdict —
// always this default rather than HTTPConfig.MaxResponseBytes, for the reason
// closeResponse gives.
const DefaultMaxResponseBytes int64 = 1 << 20

// DocumentTerminator ends each document a WRITER-bound OTLP exporter emits, so
// a stream of exports is newline-delimited JSON. It is deliberately NOT part of
// what Marshal returns: that is the HTTP body, and a body is one document.
const DocumentTerminator byte = '\n'
