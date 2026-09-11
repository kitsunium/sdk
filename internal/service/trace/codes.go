// Package trace — range 0.3.50.* (ADR 0051 service/trace block).
package trace

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.50.0 - 0.3.50.255

// CodeEntropyFailed identifies a crypto/rand.Read failure while drawing the
// bytes of a trace or span identifier.
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
