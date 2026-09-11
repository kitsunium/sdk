// Package metrics — the Exporter contract + process-wide exporter registry.
package metrics

import (
	"maps"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/plugin"
	ksnap "github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// ExporterName is the typed key under which an Exporter registers (e.g. "text",
// "prometheus"). The zero value ExporterName("") is reserved invalid.
type ExporterName string

// String returns the raw name.
func (n ExporterName) String() string {
	//: direct cast back to a plain string.
	return string(n)
}

// Exporter ships a Snapshot to a backend (text writer, Prometheus, OTLP, …).
// Implementations MUST be safe for concurrent use.
//
// IFACE-PLUGIN: the registry stores plug-in exporters behind this interface;
// concrete exporter types stay unexported per package.
type Exporter interface {
	Name() ExporterName
	Export(snap SnapshotValue) error
}

// registry maps each ExporterName to its Exporter (read-mostly snapshot.Value).
var registry ksnap.Value[map[ExporterName]Exporter]

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
	//: publish; a conflict panics at boot.
	if err := publishExporter(e.Name(), e); err != nil {
		//: surface the doc code.
		panic(err.Error())
	}
	//: hand back for singleton binding.
	return e
}

// publishExporter inserts (name -> e) under the writer lock (idempotent on same).
func publishExporter(name ExporterName, e Exporter) error {
	//: dupErr escapes the Update closure on a conflict.
	var dupErr error
	//: Update serialises writers so check + publish are atomic.
	registry.Update(func(current *map[ExporterName]Exporter) *map[ExporterName]Exporter {
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
func cloneExporterMap(src *map[ExporterName]Exporter, name ExporterName, e Exporter) map[ExporterName]Exporter {
	//: size hint = source + 1.
	var size int
	//: nil source is the first registration.
	if src != nil {
		//: pre-size for existing entries plus one.
		size = len(*src)
	}
	//: allocate the new snapshot.
	next := make(map[ExporterName]Exporter, size+1)
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

// LookupExporter returns the Exporter registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in exporters behind the Exporter
// interface — concrete backend types stay unexported.
func LookupExporter(name ExporterName) (e Exporter, ok bool) {
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
		Private: "core/metrics.Export: the registered exporter returned an error",
	})
}
