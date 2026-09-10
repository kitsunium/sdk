// Package trace — declares the sentinel *errs.Error values. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
package trace

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// InvalidTraceParent is returned when a traceparent header cannot be read.
	// Its Public never echoes the header: it is attacker-controlled text.
	InvalidTraceParent = errs.Define(CodeInvalidTraceParent, "INVALID_TRACE_PARENT",
		"The traceparent header is not a valid W3C trace context",
		"core/trace.ParseTraceParent: the header failed the W3C Trace Context grammar, or carried an all-zero trace-id or parent-id")

	// InvalidTraceState is returned when a tracestate header cannot be read.
	InvalidTraceState = errs.Define(CodeInvalidTraceState, "INVALID_TRACE_STATE",
		"The tracestate header is not a valid W3C trace state list",
		"core/trace.ParseTraceState: the header failed the W3C list grammar, exceeded 32 members, or repeated a key")

	// UnknownExporter is returned when no SpanExporter is registered under a Name.
	UnknownExporter = errs.Define(CodeUnknownExporter, "UNKNOWN_EXPORTER",
		"No trace exporter is registered under that name",
		"core/trace.Export: exporter absent from registry; blank-import the exporter's package")

	// ExportFailed wraps an exporter's failure while shipping spans.
	ExportFailed = errs.Define(CodeExportFailed, "EXPORT_FAILED",
		"The trace exporter failed to ship the spans",
		"core/trace.Export: the registered exporter returned an error")

	// DuplicateRegistration is the boot-time SpanExporter-registry panic sentinel.
	DuplicateRegistration = errs.Define(CodeDuplicateRegistration, "DUPLICATE_REGISTRATION",
		"A trace exporter is already registered under that name",
		"core/trace.RegisterExporter: a distinct exporter already claims this name, or a nil exporter was supplied")

	// InvalidSpanName is the panic sentinel for a Start call with no name.
	InvalidSpanName = errs.Define(CodeInvalidSpanName, "INVALID_SPAN_NAME",
		"A span name must not be empty",
		"core/trace: Tracer.Start requires a low-cardinality operation name; an empty one produces a trace no backend can group")
)
