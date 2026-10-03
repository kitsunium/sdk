// Package trace — range 0.2.20.* (ADR 0051 core/observe/trace block), and
// range 0.3.50.*, the tracing engine's, declared here since ADR 0160.
package trace

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.20.0 - 0.2.20.255

// CodeInvalidTraceParent identifies a traceparent header that cannot be read as
// a span context: a malformed field, the invalid version "ff", or an all-zero
// trace-id or parent-id — the two values W3C Trace Context declares invalid.
const CodeInvalidTraceParent errs.Code = 0x00_02_14_01 // 0.2.20.1

// CodeInvalidTraceState identifies a tracestate header that does not match the
// W3C list grammar: a key or value outside its character set, more than 32 list
// members, or the same key twice.
const CodeInvalidTraceState errs.Code = 0x00_02_14_02 // 0.2.20.2

// CodeUnknownExporter identifies an Export/Lookup naming a span exporter that no
// imported package has registered.
const CodeUnknownExporter errs.Code = 0x00_02_14_03 // 0.2.20.3

// CodeExportFailed identifies a span exporter that returned an error while
// shipping a batch (the exporter's cause rides the wrap trail).
const CodeExportFailed errs.Code = 0x00_02_14_04 // 0.2.20.4

// CodeDuplicateRegistration identifies a boot-time SpanExporter registry
// collision: a nil exporter, or a distinct exporter claiming a taken Name.
const CodeDuplicateRegistration errs.Code = 0x00_02_14_05 // 0.2.20.5

// CodeInvalidSpanName identifies a Start call with an empty span name. A span's
// name is the low-cardinality operation label every backend groups on, so an
// empty one produces a trace nobody can search for.
const CodeInvalidSpanName errs.Code = 0x00_02_14_06 // 0.2.20.6

// CodeInvalidAttribute identifies an attribute set a span, an event, a link or
// a Resource cannot carry: an attribute with an empty Key, the same Key twice,
// or a value no constructor ever set (AttrKindInvalid). The rules are the
// shared model's (internal/core/observe/otel); this code is the trace signal's own,
// where it used to borrow the metrics one (0.2.9.4) because the model lived in
// internal/core/observe/metrics.
const CodeInvalidAttribute errs.Code = 0x00_02_14_07 // 0.2.20.7

// range: 0.3.50.0 - 0.3.50.255 — allocated to the tracing engine,
// internal/service/observe/trace (ADR 0051 service/observe/trace block), and
// declared here since ADR 0160: the engine returns these and declares none.

// CodeEntropyFailed identified a crypto/rand.Read failure while drawing the
// bytes of a trace or span identifier. Nothing returns it since Go 1.24,
// whose crypto/rand.Read cannot fail; the code stays allocated and published.
const CodeEntropyFailed errs.Code = 0x00_03_32_01 // 0.3.50.1

// CodeInvalidSampleRatio identifies a Ratio sampler asked for a fraction that
// is not one: NaN, negative, above 1, or exactly 0 — which is NeverSample under
// a name that also spells "unconfigured" (ADR 0031).
const CodeInvalidSampleRatio errs.Code = 0x00_03_32_02 // 0.3.50.2

// CodeOTLPInvalidSpanContext identifies a span handed to the OTLP/JSON encoder
// whose trace-id or span-id is all zeroes. Both are declared invalid by W3C
// Trace Context and required by the schema, so there is no honest hex to emit.
const CodeOTLPInvalidSpanContext errs.Code = 0x00_03_32_03 // 0.3.50.3

// CodeOTLPSpanNotEnded identifies a span handed to the OTLP/JSON encoder with no
// EndTime. `end_time_unix_nano` is required, and a zero would claim the span
// ended at the Unix epoch.
const CodeOTLPSpanNotEnded errs.Code = 0x00_03_32_04 // 0.3.50.4

// CodeOTLPEndpointInvalid identifies an OTLP/HTTP exporter configured with an
// endpoint that is not an absolute http(s) URL carrying a path — the address
// the POST would otherwise be sent to blind.
const CodeOTLPEndpointInvalid errs.Code = 0x00_03_32_05 // 0.3.50.5

// CodeOTLPExportRejected identifies a collector that refused the payload
// PERMANENTLY: an HTTP status outside the specification's retryable set, so
// replaying the same bytes would fail the same way.
const CodeOTLPExportRejected errs.Code = 0x00_03_32_06 // 0.3.50.6

// CodeOTLPExportUnavailable identifies a TRANSIENT OTLP/HTTP failure — a
// transport fault, or one of the four statuses the specification lists as
// retryable. The same bytes may succeed later.
const CodeOTLPExportUnavailable errs.Code = 0x00_03_32_07 // 0.3.50.7

// CodeOTLPPartialSuccess identifies a collector that accepted the request
// (HTTP 200) but rejected some of its spans. The specification forbids retrying
// it, so the loss is reported rather than replayed.
const CodeOTLPPartialSuccess errs.Code = 0x00_03_32_08 // 0.3.50.8
