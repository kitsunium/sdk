// Package metrics — range 0.2.9.* (ADR 0027 core/observe/metrics block), and
// range 0.3.45.*, the metrics engine's, declared here since ADR 0160.
package metrics

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.9.0 - 0.2.9.255

// CodeUnknownExporter identifies an Export/Lookup naming an exporter that no
// imported package has registered.
const CodeUnknownExporter errs.Code = 0x00_02_09_01 // 0.2.9.1

// CodeExportFailed identifies an exporter that returned an error while shipping
// a snapshot (the exporter's cause rides the wrap trail).
const CodeExportFailed errs.Code = 0x00_02_09_02 // 0.2.9.2

// CodeInstrumentKindConflict identifies a Meter call reusing a name already
// bound to a DIFFERENT instrument kind (e.g. a Counter name fetched as a Gauge,
// or as an UpDownCounter, which is a different monotonicity on one metric).
const CodeInstrumentKindConflict errs.Code = 0x00_02_09_03 // 0.2.9.3

// CodeInvalidAttribute identifies an instrument fetched with an attribute set
// that cannot name a series: an attribute with an empty Key, the same Key
// twice, or a value no constructor ever set (AttrKindInvalid).
const CodeInvalidAttribute errs.Code = 0x00_02_09_04 // 0.2.9.4

// CodeDuplicateRegistration identifies a boot-time Exporter registry collision:
// a nil exporter, or a distinct exporter claiming an already-registered Name.
const CodeDuplicateRegistration errs.Code = 0x00_02_09_05 // 0.2.9.5

// CodeInvalidTemporality identifies a MeterConfig carrying a Temporality that
// is none of the three declared constants — reachable only by a deliberate cast.
const CodeInvalidTemporality errs.Code = 0x00_02_09_06 // 0.2.9.6

// CodeInvalidDescription identifies a Describe call carrying an empty
// description — a call that would document nothing (ADR 0067).
const CodeInvalidDescription errs.Code = 0x00_02_09_07 // 0.2.9.7

// CodeDescriptionConflict identifies a second, DIFFERENT description bound to
// an instrument name that already has one. A description belongs to the name,
// so two of them means one of the two wiring sites is wrong (ADR 0067).
const CodeDescriptionConflict errs.Code = 0x00_02_09_08 // 0.2.9.8

// range: 0.3.45.0 - 0.3.45.255 — allocated to the metrics engine,
// internal/service/observe/metrics (ADR 0005 service/observe/metrics block),
// and declared here since ADR 0160: the engine returns these and declares
// none. Every one of them is about a wire — Prometheus text or OTLP — the
// engine writes.

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

// CodeUnsupportedTemporality identifies a snapshot that is not cumulative —
// delta, or an unresolved temporality — handed to the Prometheus exporter,
// whose exposition format has no temporality field and whose server reads
// every counter as cumulative.
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
