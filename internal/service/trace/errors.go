// Package trace — declares the sentinel *errs.Error values. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
package trace

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// EntropyFailed wraps a crypto/rand.Read failure while drawing id bytes.
	EntropyFailed = errs.Define(CodeEntropyFailed, "ENTROPY_FAILED",
		"Trace identifier generation failed to read secure random bytes",
		"service/trace: crypto/rand.Read returned an error")

	// InvalidSampleRatio is returned when Ratio is handed a fraction it will
	// not turn into a sampler.
	InvalidSampleRatio = errs.Define(CodeInvalidSampleRatio, "INVALID_SAMPLE_RATIO",
		"A sampling ratio must be greater than 0 and at most 1",
		"service/trace.Ratio: use NeverSample for none and AlwaysSample for all; 0 also spells an unset field, so it is refused rather than guessed")

	// OTLPInvalidSpanContext is returned when a span carries an unusable id.
	OTLPInvalidSpanContext = errs.Define(CodeOTLPInvalidSpanContext, "OTLP_INVALID_SPAN_CONTEXT",
		"A span carries an all-zero trace or span identifier",
		"service/trace: OTLP requires 16 trace-id bytes and 8 span-id bytes, and W3C Trace Context declares the all-zero form of each invalid")

	// OTLPSpanNotEnded is returned when a span reaches the encoder unended.
	OTLPSpanNotEnded = errs.Define(CodeOTLPSpanNotEnded, "OTLP_SPAN_NOT_ENDED",
		"A span reached the exporter without an end time",
		"service/trace: endTimeUnixNano is required; a zero would claim the span ended at the Unix epoch")

	// OTLPEndpointInvalid is returned when an OTLP/HTTP endpoint is unusable.
	OTLPEndpointInvalid = errs.Define(CodeOTLPEndpointInvalid, "OTLP_ENDPOINT_INVALID",
		"The OTLP endpoint must be an absolute http(s) URL with a path",
		"service/trace.NewOTLPHTTPExporter: a bare host connects, answers 404 and looks exactly like a collector that is up")

	// OTLPExportRejected reports a permanent refusal by the collector.
	OTLPExportRejected = errs.Define(CodeOTLPExportRejected, "OTLP_EXPORT_REJECTED",
		"The trace collector rejected the export",
		"service/trace: the collector answered a status the OTLP specification says MUST NOT be retried")

	// OTLPExportUnavailable reports a transient failure worth retrying.
	OTLPExportUnavailable = errs.Define(CodeOTLPExportUnavailable, "OTLP_EXPORT_UNAVAILABLE",
		"The trace collector is unavailable",
		"service/trace: a transport fault, or one of the four statuses the OTLP specification lists as retryable")

	// OTLPPartialSuccess reports an accepted request with rejected spans.
	OTLPPartialSuccess = errs.Define(CodeOTLPPartialSuccess, "OTLP_PARTIAL_SUCCESS",
		"The trace collector accepted the request but rejected some spans",
		"service/trace: the OTLP specification forbids retrying a partial success, so the loss is reported rather than replayed")
)
