package trace

import (
	"context"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/plugin"
)

// DefaultExportTimeout bounds one export when the caller's context carries no
// deadline of its own: 30 s, the OpenTelemetry trace SDK specification's default
// for a batch processor's exportTimeout (OTEL_BSP_EXPORT_TIMEOUT). The
// specification says an export MUST NOT block indefinitely, and a context with
// no deadline is how an indefinite block is written; a caller who wants another
// bound puts it on the context.
const DefaultExportTimeout time.Duration = 30 * time.Second

// ExporterName is the typed key under which a SpanExporter registers (e.g.
// "otlpjson"). The zero value ExporterName("") is reserved invalid.
//
// The registry is deliberately the same shape as core/observe/metrics' — the same
// kernel/plugin.Registry underneath, named, reached by blank-importing the
// exporter's package, config-addressable. It EARNS its
// keep here for the reason it earns it there and for one more: ADR 0051
// §Decision 5 requires the OTLP/HTTP emitter to be absent from it, and "absent
// from the registry" is only a statement anyone can check if there is a registry
// to be absent from. A test pins that absence.
type ExporterName string

// String returns the raw name.
func (n ExporterName) String() string {
	//: direct cast back to a plain string.
	return string(n)
}

// registry maps each ExporterName to its SpanExporter — a read-mostly,
// copy-on-write table (kernel/plugin.Registry), the same mechanism the other
// signal's exporter registry runs on.
var registry plugin.Registry[ExporterName, SpanExporter]

// RegisterExporter inserts e under e.Name() and returns it for singleton
// binding. Panics on a nil exporter or a distinct exporter on a taken Name.
//
// IFACE-PLUGIN: the registry hands plug-in SpanExporter instances back to
// callers so each backend keeps its concrete type unexported.
//
// "Nil" here means UNUSABLE, not only an untyped nil: a typed nil pointer and a
// plug-in whose type is not comparable both satisfy the port and neither can
// serve one call (see internal/kernel/plugin).
func RegisterExporter(e SpanExporter) SpanExporter {
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

// LookupExporter returns the SpanExporter registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in exporters behind the SpanExporter
// interface — concrete backend types stay unexported.
func LookupExporter(name ExporterName) (e SpanExporter, ok bool) {
	//: a snapshot read; a miss hands back nil AND false.
	return registry.Lookup(name)
}

// AvailableExporters returns the sorted list of registered ExporterNames.
func AvailableExporters() []ExporterName {
	//: sorted, and nil before any Register.
	return registry.Names()
}

// Export ships spans through the SpanExporter registered as name. A missing
// exporter returns UnknownExporter; an exporter failure wraps as ExportFailed.
func Export(name ExporterName, spans SpansValue) error {
	//: resolve the exporter first.
	exporter, ok := LookupExporter(name)
	//: absence path — surface the typed sentinel.
	if !ok {
		//: name not registered.
		return UnknownExporter
	}
	//: delegate the export.
	err := exporter.Export(spans)
	//: success fast-path.
	if err == nil {
		//: shipped cleanly.
		return nil
	}
	//: wrap the exporter failure with the dotted-quad code.
	return errs.Wrap(err, errs.WrapParams{
		Code:    CodeExportFailed,
		Reason:  "EXPORT_FAILED",
		Public:  "The trace exporter failed to ship the spans",
		Private: "core/observe/trace.Export: the registered exporter returned an error",
	})
}

// ExportContext ships spans through the SpanExporter registered as name, bounded
// by ctx. A missing exporter returns UnknownExporter; an exporter failure wraps
// as ExportFailed.
//
// When ctx carries no deadline, DefaultExportTimeout is applied: the
// specification forbids an export that blocks indefinitely. An exporter that
// implements ContextSpanExporter receives the bounded context through
// ExportContext; one that implements only SpanExporter is called through Export,
// which has no context to honour, and is bounded by nothing but itself — every
// exporter that touches a network in this SDK implements the sibling.
func ExportContext(ctx context.Context, name ExporterName, spans SpansValue) error {
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
	//: the sibling receives the bound; a bare SpanExporter cannot.
	var err error
	//: prefer the context-aware sibling when the exporter implements it.
	if withContext, isContext := exporter.(ContextSpanExporter); isContext {
		//: the bounded export.
		err = withContext.ExportContext(bounded, spans)
	} else {
		//: no context to honour — the exporter bounds itself or nothing does.
		err = exporter.Export(spans)
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
		Public:  "The trace exporter failed to ship the spans",
		Private: "core/observe/trace.ExportContext: the registered exporter returned an error",
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
