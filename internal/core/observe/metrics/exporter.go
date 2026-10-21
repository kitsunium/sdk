package metrics

import (
	"context"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/plugin"
)

// DefaultExportTimeout bounds one export when the caller's context carries no
// deadline of its own: 30 s, the OpenTelemetry metrics SDK specification's
// default exportTimeout for a periodic exporting reader
// (OTEL_METRIC_EXPORT_TIMEOUT). A context with no deadline is how an
// indefinite block is written; a caller who wants another bound puts it on the
// context.
const DefaultExportTimeout time.Duration = 30 * time.Second

// ExporterName is the typed key under which an Exporter registers (e.g. "text",
// "prometheus"). The zero value ExporterName("") is reserved invalid.
type ExporterName string

// String returns the raw name.
func (n ExporterName) String() string {
	//: direct cast back to a plain string.
	return string(n)
}

// registry maps each ExporterName to its Exporter — a read-mostly,
// copy-on-write table (kernel/plugin.Registry), the same mechanism the other
// signal's exporter registry runs on.
var registry plugin.Registry[ExporterName, Exporter]

// RegisterExporter inserts e under e.Name() and returns it for singleton
// binding. Panics on a nil exporter or a distinct exporter on a taken Name.
//
// IFACE-PLUGIN: the registry hands plug-in Exporter instances back to callers so
// each backend keeps its concrete type unexported.
//
// "Nil" here means UNUSABLE, not only an untyped nil: a typed nil pointer and a
// plug-in whose type is not comparable both satisfy the port and neither can
// serve one call (see internal/kernel/plugin).
func RegisterExporter(e Exporter) Exporter {
	//: a typed nil and a non-comparable plug-in both satisfy the port and
	//: neither can serve — refuse at import, where the offender is named.
	if why := plugin.Unusable(e); why != "" {
		//: panic with the dotted-quad code at boot.
		panic(DuplicateRegistration.Error() + ": " + why)
	}
	//: publish; a DISTINCT exporter on a taken name is the hard conflict,
	//: refused at boot under this signal's own code. The same exporter
	//: registered twice is a no-op, so a diamond import is not a panic.
	if registry.Publish(e.Name(), e) {
		//: surface the doc code, naming the exporter.
		panic(errs.Wrap(DuplicateRegistration, errs.WrapParams{}, errs.String("exporter", string(e.Name()))).Error())
	}
	//: hand back for singleton binding.
	return e
}

// LookupExporter returns the Exporter registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in exporters behind the Exporter
// interface — concrete backend types stay unexported.
func LookupExporter(name ExporterName) (e Exporter, ok bool) {
	//: a snapshot read; a miss hands back nil AND false.
	return registry.Lookup(name)
}

// AvailableExporters returns the sorted list of registered ExporterNames.
func AvailableExporters() []ExporterName {
	//: sorted, and nil before any Register.
	return registry.Names()
}

// Export ships snap through the Exporter registered as name. A missing exporter
// returns UnknownExporter; an exporter failure wraps as ExportFailed.
func Export(name ExporterName, snap SnapshotValue) error {
	//: resolve the exporter first.
	exporter, ok := LookupExporter(name)
	//: absence path — surface the typed sentinel.
	if !ok {
		//: name not registered.
		return UnknownExporter
	}
	//: delegate the export.
	err := exporter.Export(snap)
	//: success fast-path.
	if err == nil {
		//: shipped cleanly.
		return nil
	}
	//: wrap the exporter failure with the dotted-quad code.
	return errs.Wrap(err, errs.WrapParams{
		Code:    CodeExportFailed,
		Reason:  "EXPORT_FAILED",
		Public:  "The metrics exporter failed to ship the snapshot",
		Private: "core/observe/metrics.Export: the registered exporter returned an error",
	})
}

// ExportContext ships snap through the Exporter registered as name, bounded by
// ctx. A missing exporter returns UnknownExporter; an exporter failure wraps as
// ExportFailed.
//
// When ctx carries no deadline, DefaultExportTimeout is applied: the
// specification bounds every export. An exporter that implements ContextExporter
// receives the bounded context through ExportContext; one that implements only
// Exporter is called through Export, which has no context to honour, and is
// bounded by nothing but itself — every exporter that touches a network in this
// SDK implements the sibling.
func ExportContext(ctx context.Context, name ExporterName, snap SnapshotValue) error {
	//: resolve the exporter first, exactly as Export does.
	exporter, ok := LookupExporter(name)
	//: absence path — surface the typed sentinel.
	if !ok {
		//: name not registered.
		return UnknownExporter
	}
	//: a context with no deadline is an indefinite block; bound it with the
	//: specification's default rather than letting the export hang.
	bounded, cancel := boundExport(ctx)
	defer cancel()
	//: the sibling receives the bound; a bare Exporter cannot.
	var err error
	//: prefer the context-aware sibling when the exporter implements it.
	if withContext, isContext := exporter.(ContextExporter); isContext {
		//: the bounded export.
		err = withContext.ExportContext(bounded, snap)
	} else {
		//: no context to honour — the exporter bounds itself or nothing does.
		err = exporter.Export(snap)
	}
	//: success fast-path.
	if err == nil {
		//: shipped cleanly.
		return nil
	}
	//: wrap the exporter failure with the dotted-quad code.
	return errs.Wrap(err, errs.WrapParams{
		Code:    CodeExportFailed,
		Reason:  "EXPORT_FAILED",
		Public:  "The metrics exporter failed to ship the snapshot",
		Private: "core/observe/metrics.ExportContext: the registered exporter returned an error",
	})
}

// boundExport returns ctx unchanged when it already carries a deadline, and
// ctx bounded by DefaultExportTimeout when it does not, with the function that
// releases it.
func boundExport(ctx context.Context) (context.Context, context.CancelFunc) {
	//: the caller's own deadline is the bound — never lengthened, never replaced.
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		//: nothing to release.
		return ctx, func() {}
	}
	//: the specification's default bound.
	return context.WithTimeout(ctx, DefaultExportTimeout)
}
