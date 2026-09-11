// Package metrics — range 0.3.45.* (ADR 0005 service/metrics block).
package metrics

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.45.0 - 0.3.45.255

// CodeInvalidMetricName identifies an instrument name the Prometheus text
// exposition format cannot carry: it does not match [a-zA-Z_:][a-zA-Z0-9_:]*.
const CodeInvalidMetricName errs.Code = 0x00_03_2D_01 // 0.3.45.1

// CodeInvalidLabelName identifies an attribute key the Prometheus text
// exposition format cannot carry: it does not match [a-zA-Z_][a-zA-Z0-9_]* (a
// metric name may hold a colon, a label name may not — and neither may hold the
// dot every OTel-conventional attribute key is spelled with).
const CodeInvalidLabelName errs.Code = 0x00_03_2D_02 // 0.3.45.2

// CodeReservedLabelName identifies a syntactically legal attribute key that the
// exposition format or the Prometheus server reserves for its own use: the "__"
// prefix, or "le" on a histogram, where it names the bucket's upper bound.
const CodeReservedLabelName errs.Code = 0x00_03_2D_03 // 0.3.45.3

// CodeUnsupportedTemporality identifies a delta snapshot handed to the
// Prometheus exporter, whose exposition format has no temporality field and
// whose server reads every counter as cumulative.
const CodeUnsupportedTemporality errs.Code = 0x00_03_2D_04 // 0.3.45.4

// CodeOTLPUnresolvedTemporality identifies a snapshot handed to the OTLP/JSON
// encoder carrying a temporality that is neither delta nor cumulative. The
// schema's AGGREGATION_TEMPORALITY_UNSPECIFIED "MUST not be used", so there is
// no honest integer to emit.
const CodeOTLPUnresolvedTemporality errs.Code = 0x00_03_2D_05 // 0.3.45.5

// CodeOTLPInvalidBucketLayout identifies a histogram point whose buckets OTLP
// cannot express: a count array that is not one longer than the bounds array,
// or bounds that are non-finite or not strictly increasing.
const CodeOTLPInvalidBucketLayout errs.Code = 0x00_03_2D_06 // 0.3.45.6

// CodeOTLPEndpointInvalid identifies an OTLP/HTTP exporter configured with an
// endpoint that is not an absolute http(s) URL carrying a path — the address
// the POST would otherwise be sent to blind.
const CodeOTLPEndpointInvalid errs.Code = 0x00_03_2D_07 // 0.3.45.7

// CodeOTLPExportRejected identifies a collector that refused the payload
// PERMANENTLY: an HTTP status outside the specification's retryable set, so
// replaying the same bytes would fail the same way.
const CodeOTLPExportRejected errs.Code = 0x00_03_2D_08 // 0.3.45.8

// CodeOTLPExportUnavailable identifies a TRANSIENT OTLP/HTTP failure — a
// transport fault, or one of the four statuses the specification lists as
// retryable. The same bytes may succeed later.
const CodeOTLPExportUnavailable errs.Code = 0x00_03_2D_09 // 0.3.45.9

// CodeOTLPPartialSuccess identifies a collector that accepted the request
// (HTTP 200) but rejected some of its data points. The specification forbids
// retrying it, so the loss is reported rather than replayed.
const CodeOTLPPartialSuccess errs.Code = 0x00_03_2D_0A // 0.3.45.10
