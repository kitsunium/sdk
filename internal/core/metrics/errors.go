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

	// InvalidAttribute is the panic sentinel for an unusable attribute set.
	InvalidAttribute = errs.Define(CodeInvalidAttribute, "INVALID_ATTRIBUTE",
		"An attribute key is empty or repeated, or its value was never set",
		"service/metrics: an attribute set must name each dimension once, with a non-empty key and a value from String/Bool/Int64/Float64")

	// DuplicateRegistration is the boot-time Exporter-registry panic sentinel.
	DuplicateRegistration = errs.Define(CodeDuplicateRegistration, "DUPLICATE_REGISTRATION",
		"An exporter is already registered under that name",
		"core/metrics.RegisterExporter: a distinct exporter already claims this name, or a nil exporter was supplied")

	// InvalidTemporality is the panic sentinel for a Temporality that is none
	// of the three declared constants.
	InvalidTemporality = errs.Define(CodeInvalidTemporality, "INVALID_TEMPORALITY",
		"That aggregation temporality is not one this SDK declares",
		"core/metrics: Temporality must be Unspecified, Delta or Cumulative; any other value came from a cast")

	// InvalidDescription is the panic sentinel for an empty Describe.
	InvalidDescription = errs.Define(CodeInvalidDescription, "INVALID_DESCRIPTION",
		"An instrument description must not be empty",
		"service/metrics: Describer.Describe was handed \"\"; a description that documents nothing is the inert call ADR 0031 bans")

	// DescriptionConflict is the panic sentinel for two different descriptions
	// bound to one instrument name.
	DescriptionConflict = errs.Define(CodeDescriptionConflict, "DESCRIPTION_CONFLICT",
		"That instrument name already carries a different description",
		"service/metrics: a description belongs to the name; re-describing with identical text is idempotent, differing text is a wiring defect")
)
