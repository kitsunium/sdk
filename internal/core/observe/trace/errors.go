// Package trace — declares the sentinel *errs.Error values. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
package trace

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// InvalidTraceParent is returned when a traceparent header cannot be read.
	// Its Public never echoes the header: it is attacker-controlled text.
	InvalidTraceParent = errs.Define(CodeInvalidTraceParent, "INVALID_TRACE_PARENT",
		"The traceparent header is not a valid W3C trace context",
		"core/observe/trace.ParseTraceParent: the header failed the W3C Trace Context grammar, or carried an all-zero trace-id or parent-id")

	// InvalidTraceState is returned when a tracestate header cannot be read.
	InvalidTraceState = errs.Define(CodeInvalidTraceState, "INVALID_TRACE_STATE",
		"The tracestate header is not a valid W3C trace state list",
		"core/observe/trace.ParseTraceState: the header failed the W3C list grammar, exceeded 32 members, or repeated a key")

	// UnknownExporter is returned when no SpanExporter is registered under a Name.
	UnknownExporter = errs.Define(CodeUnknownExporter, "UNKNOWN_EXPORTER",
		"No trace exporter is registered under that name",
		"core/observe/trace.Export: exporter absent from registry; blank-import the exporter's package")

	// ExportFailed wraps an exporter's failure while shipping spans.
	ExportFailed = errs.Define(CodeExportFailed, "EXPORT_FAILED",
		"The trace exporter failed to ship the spans",
		"core/observe/trace.Export: the registered exporter returned an error")

	// DuplicateRegistration is the boot-time SpanExporter-registry panic sentinel.
	DuplicateRegistration = errs.Define(CodeDuplicateRegistration, "DUPLICATE_REGISTRATION",
		"A trace exporter is already registered under that name",
		"core/observe/trace.RegisterExporter: a distinct exporter already claims this name, or a nil exporter was supplied")

	// InvalidSpanName is the panic sentinel for a Start call with no name.
	InvalidSpanName = errs.Define(CodeInvalidSpanName, "INVALID_SPAN_NAME",
		"A span name must not be empty",
		"core/observe/trace: Tracer.Start requires a low-cardinality operation name; an empty one produces a trace no backend can group")

	// InvalidAttribute is the panic sentinel for an unusable attribute set on
	// a span, an event, a link or a Resource.
	InvalidAttribute = errs.Define(CodeInvalidAttribute, "INVALID_ATTRIBUTE",
		"An attribute key is empty or repeated, or its value was never set",
		"core/observe/trace: an attribute set must name each dimension once, with a non-empty key and a value from String/Bool/Int64/Float64")

	// EntropyFailed wraps a crypto/rand.Read failure while drawing id bytes.
	EntropyFailed = errs.Define(CodeEntropyFailed, "ENTROPY_FAILED",
		"Trace identifier generation failed to read secure random bytes",
		"service/observe/trace: crypto/rand.Read returned an error")

	// InvalidSampleRatio is returned when Ratio is handed a fraction it will
	// not turn into a sampler.
	InvalidSampleRatio = errs.Define(CodeInvalidSampleRatio, "INVALID_SAMPLE_RATIO",
		"A sampling ratio must be greater than 0 and at most 1",
		"service/observe/trace.Ratio: use NeverSample for none and AlwaysSample for all; 0 also spells an unset field, so it is refused rather than guessed")

	// OTLPInvalidSpanContext is returned when a span carries an unusable id.
	OTLPInvalidSpanContext = errs.Define(CodeOTLPInvalidSpanContext, "OTLP_INVALID_SPAN_CONTEXT",
		"A span carries an all-zero trace or span identifier",
		"service/observe/trace: OTLP requires 16 trace-id bytes and 8 span-id bytes, and W3C Trace Context declares the all-zero form of each invalid")

	// OTLPSpanNotEnded is returned when a span reaches the encoder unended.
	OTLPSpanNotEnded = errs.Define(CodeOTLPSpanNotEnded, "OTLP_SPAN_NOT_ENDED",
		"A span reached the exporter without an end time",
		"service/observe/trace: endTimeUnixNano is required; a zero would claim the span ended at the Unix epoch")

	// OTLPEndpointInvalid is returned when an OTLP/HTTP endpoint is unusable.
	OTLPEndpointInvalid = errs.Define(CodeOTLPEndpointInvalid, "OTLP_ENDPOINT_INVALID",
		"The OTLP endpoint must be an absolute http(s) URL with a path",
		"service/observe/trace.NewOTLPHTTPExporter: a bare host connects, answers 404 and looks exactly like a collector that is up")

	// OTLPExportRejected reports a permanent refusal by the collector.
	OTLPExportRejected = errs.Define(CodeOTLPExportRejected, "OTLP_EXPORT_REJECTED",
		"The trace collector rejected the export",
		"service/observe/trace: the collector answered a status the OTLP specification says MUST NOT be retried")

	// OTLPExportUnavailable reports a transient failure worth retrying.
	OTLPExportUnavailable = errs.Define(CodeOTLPExportUnavailable, "OTLP_EXPORT_UNAVAILABLE",
		"The trace collector is unavailable",
		"service/observe/trace: a transport fault, or one of the four statuses the OTLP specification lists as retryable")

	// OTLPPartialSuccess reports an accepted request with rejected spans.
	OTLPPartialSuccess = errs.Define(CodeOTLPPartialSuccess, "OTLP_PARTIAL_SUCCESS",
		"The trace collector accepted the request but rejected some spans",
		"service/observe/trace: the OTLP specification forbids retrying a partial success, so the loss is reported rather than replayed")
)
