// Package trace — the SpanExporter contract + process-wide exporter registry.
package trace

import (
	"maps"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	ksnap "github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// ExporterName is the typed key under which a SpanExporter registers (e.g.
// "otlpjson"). The zero value ExporterName("") is reserved invalid.
//
// The registry is deliberately the same shape as core/metrics' — named, reached
// by blank-importing the exporter's package, config-addressable. It EARNS its
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

// SpanExporter ships a batch of finished spans to a backend.
// Implementations MUST be safe for concurrent use.
//
// IFACE-PLUGIN: the registry stores plug-in exporters behind this interface;
// concrete exporter types stay unexported per package.
type SpanExporter interface {
	Name() ExporterName
	Export(spans SpansValue) error
}

// registry maps each ExporterName to its SpanExporter (read-mostly
// snapshot.Value).
var registry ksnap.Value[map[ExporterName]SpanExporter]

// RegisterExporter inserts e under e.Name() and returns it for singleton
// binding. Panics on a nil exporter or a distinct exporter on a taken Name.
//
// IFACE-PLUGIN: the registry hands plug-in SpanExporter instances back to
// callers so each backend keeps its concrete type unexported.
func RegisterExporter(e SpanExporter) SpanExporter {
	//: nil registration is always a programming error.
	if e == nil {
		//: panic with the dotted-quad code at boot.
		panic(DuplicateRegistration.Error())
	}
	//: publish; a conflict panics at boot.
	if err := publishExporter(e.Name(), e); err != nil {
		//: surface the doc code.
		panic(err.Error())
	}
	//: hand back for singleton binding.
	return e
}

// publishExporter inserts (name -> e) under the writer lock (idempotent on same).
func publishExporter(name ExporterName, e SpanExporter) error {
	//: dupErr escapes the Update closure on a conflict.
	var dupErr error
	//: Update serialises writers so check + publish are atomic.
	registry.Update(func(current *map[ExporterName]SpanExporter) *map[ExporterName]SpanExporter {
		//: duplicate detection before any alloc.
		if current != nil {
			//: an existing entry decides idempotent vs conflict.
			if existing, dup := (*current)[name]; dup {
				//: re-registering the SAME exporter is a no-op.
				if existing == e {
					//: keep the current snapshot.
					return current
				}
				//: a DISTINCT exporter on a taken name is the hard conflict.
				dupErr = errs.Wrap(DuplicateRegistration, errs.WrapParams{}, errs.String("exporter", string(name)))
				//: republish unchanged.
				return current
			}
		}
		//: clone + insert, then publish atomically.
		return new(cloneExporterMap(current, name, e))
	})
	//: surface any conflict to RegisterExporter.
	return dupErr
}

// cloneExporterMap copies src and inserts (name -> e).
func cloneExporterMap(src *map[ExporterName]SpanExporter, name ExporterName, e SpanExporter) map[ExporterName]SpanExporter {
	//: size hint = source + 1.
	var size int
	//: nil source is the first registration.
	if src != nil {
		//: pre-size for existing entries plus one.
		size = len(*src)
	}
	//: allocate the new snapshot.
	next := make(map[ExporterName]SpanExporter, size+1)
	//: bulk-copy the existing entries.
	if src != nil {
		//: copy forward.
		maps.Copy(next, *src)
	}
	//: insert the new entry.
	next[name] = e
	//: caller publishes via Value.Update.
	return next
}

// LookupExporter returns the SpanExporter registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in exporters behind the SpanExporter
// interface — concrete backend types stay unexported.
func LookupExporter(name ExporterName) (e SpanExporter, ok bool) {
	//: load the current snapshot; nil before first Register.
	current := registry.Load()
	//: absence path.
	if current == nil {
		//: clean miss.
		return nil, false
	}
	//: typed map read.
	exporter, found := (*current)[name]
	//: hand back the result.
	return exporter, found
}

// AvailableExporters returns the sorted list of registered ExporterNames.
func AvailableExporters() []ExporterName {
	//: snapshot the registry; nil before any Register.
	current := registry.Load()
	//: empty result when nothing registered.
	if current == nil {
		//: documented nil zero value.
		return nil
	}
	//: collect + sort the keys.
	names := slices.Collect(maps.Keys(*current))
	//: deterministic order.
	slices.Sort(names)
	//: hand back the ordered slice.
	return names
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
		Private: "core/trace.Export: the registered exporter returned an error",
	})
}
