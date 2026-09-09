// Package metrics — declares the sentinel *errs.Error values. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
package metrics

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// UnknownExporter is returned when no Exporter is registered under a Name.
	UnknownExporter = errs.Define(CodeUnknownExporter, "UNKNOWN_EXPORTER",
		"No metrics exporter is registered under that name",
		"core/metrics.Export: exporter absent from registry; blank-import the exporter's package")

	// ExportFailed wraps an exporter's failure while shipping a snapshot.
	ExportFailed = errs.Define(CodeExportFailed, "EXPORT_FAILED",
		"The metrics exporter failed to ship the snapshot",
		"core/metrics.Export: the registered exporter returned an error")

	// InstrumentKindConflict is returned when a name is reused for a different kind.
	InstrumentKindConflict = errs.Define(CodeInstrumentKindConflict, "INSTRUMENT_KIND_CONFLICT",
		"That instrument name is already registered with a different kind",
		"service/metrics: a name bound to one instrument kind was fetched as another")

	// InvalidLabel is the panic sentinel for an unusable label set.
	InvalidLabel = errs.Define(CodeInvalidLabel, "INVALID_LABEL",
		"A label key is empty or repeated in that instrument's label set",
		"service/metrics: a label set must name each dimension exactly once with a non-empty key")

	// DuplicateRegistration is the boot-time Exporter-registry panic sentinel.
	DuplicateRegistration = errs.Define(CodeDuplicateRegistration, "DUPLICATE_REGISTRATION",
		"An exporter is already registered under that name",
		"core/metrics.RegisterExporter: a distinct exporter already claims this name, or a nil exporter was supplied")
)
