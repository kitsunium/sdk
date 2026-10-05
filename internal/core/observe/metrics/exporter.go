// Package metrics — declares the sentinel *errs.Error values. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// Package metrics — the Exporter contract + process-wide exporter registry.
package metrics

import (
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/plugin"
)

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
