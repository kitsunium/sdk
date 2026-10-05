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
