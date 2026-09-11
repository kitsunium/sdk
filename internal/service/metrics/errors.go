// Package metrics — declares the sentinel *errs.Error values. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
package metrics

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// InvalidMetricName is returned when an instrument name cannot be spelled
	// as a Prometheus metric name.
	InvalidMetricName = errs.Define(CodeInvalidMetricName, "INVALID_METRIC_NAME",
		"An instrument name is not a valid Prometheus metric name",
		"service/metrics: the Prometheus exposition format requires [a-zA-Z_:][a-zA-Z0-9_:]*; rename the instrument at its call site")

	// InvalidLabelName is returned when an attribute key cannot be spelled as
	// a Prometheus label name.
	InvalidLabelName = errs.Define(CodeInvalidLabelName, "INVALID_LABEL_NAME",
		"An attribute key is not a valid Prometheus label name",
		"service/metrics: the Prometheus exposition format requires [a-zA-Z_][a-zA-Z0-9_]* for a label name — no colon, and no dot, so an OTel-conventional dotted key cannot be carried")

	// ReservedLabelName is returned when an attribute key is legal but reserved.
	ReservedLabelName = errs.Define(CodeReservedLabelName, "RESERVED_LABEL_NAME",
		"An attribute key is reserved by the Prometheus exposition format",
		"service/metrics: a \"__\" prefix is reserved for the server's internal labels, and \"le\" is reserved for a histogram's bucket bound")

	// UnsupportedTemporality is returned when a snapshot that is not
	// cumulative — delta, or an unresolved temporality — is handed to the
	// Prometheus exporter.
	UnsupportedTemporality = errs.Define(CodeUnsupportedTemporality, "UNSUPPORTED_TEMPORALITY",
		"The Prometheus exposition format carries cumulative metrics only",
		"service/metrics: the text exposition format has no temporality field and the server reads every counter as cumulative; build the meter with TemporalityCumulative for this exporter")

	// OTLPUnresolvedTemporality is returned when a snapshot's temporality has
	// no OTLP enum value.
	OTLPUnresolvedTemporality = errs.Define(CodeOTLPUnresolvedTemporality, "OTLP_UNRESOLVED_TEMPORALITY",
		"That aggregation temporality has no OTLP representation",
		"service/metrics: the OTLP AggregationTemporality enum spells only delta (1) and cumulative (2); UNSPECIFIED is documented as MUST NOT be used, so a Meter resolves the knob at construction and a hand-built snapshot must too")

	// OTLPInvalidBucketLayout is returned when a histogram point's buckets
	// cannot be spelled as explicit_bounds plus bucket_counts.
	OTLPInvalidBucketLayout = errs.Define(CodeOTLPInvalidBucketLayout, "OTLP_INVALID_BUCKET_LAYOUT",
		"That histogram's buckets have no OTLP representation",
		"service/metrics: OTLP requires bucket_counts to be one longer than explicit_bounds and explicit_bounds to be strictly increasing and finite; the bucket above the last declared bound is already the +Inf overflow")

	// OTLPEndpointInvalid is returned by NewOTLPHTTPExporter when the
	// configured endpoint cannot be a metrics collector URL.
	OTLPEndpointInvalid = errs.Define(CodeOTLPEndpointInvalid, "OTLP_ENDPOINT_INVALID",
		"The OTLP endpoint is not an absolute http or https URL with a path",
		"service/metrics: OTLP/HTTP takes a full URL used as-is, so it must carry the signal path (metrics.OTLPMetricsPath); a bare host would POST to the root and collect 404s")

	// OTLPExportRejected is returned when a collector refuses the payload with
	// a status the specification marks non-retryable.
	OTLPExportRejected = errs.Define(CodeOTLPExportRejected, "OTLP_EXPORT_REJECTED",
		"The OTLP collector rejected the metrics payload",
		"service/metrics: the collector answered a 4xx/5xx outside the retryable set, so the same bytes will fail again; check the payload, the headers and the collector's own logs")

	// OTLPExportUnavailable is returned when an OTLP/HTTP export fails in a way
	// the specification says may be retried.
	OTLPExportUnavailable = errs.Define(CodeOTLPExportUnavailable, "OTLP_EXPORT_UNAVAILABLE",
		"The OTLP collector is unreachable or overloaded",
		"service/metrics: a transport fault or one of HTTP 429/502/503/504; compose resilience.NewRetry with Retryable: metrics.OTLPRetryable rather than looping here")

	// OTLPPartialSuccess is returned when a collector accepts the request but
	// rejects some of its data points.
	OTLPPartialSuccess = errs.Define(CodeOTLPPartialSuccess, "OTLP_PARTIAL_SUCCESS",
		"The OTLP collector accepted the request but rejected some data points",
		"service/metrics: the response carried partialSuccess.rejectedDataPoints; the specification forbids retrying it, so the rejected points are lost and the cause is at the collector")
)
