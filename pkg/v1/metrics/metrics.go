//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/metrics .

// Package metrics is the public facade for the SDK's observability domain — the
// natural twin of the logger. A [Collector] (from [NewMeter]) mints lock-free
// [Counter]/[Gauge]/[Histogram] instruments by name; [Collector.Collect] takes a
// [Snapshot] that an [Exporter] ships out. The stdlib text exporter
// (name "text", stdout) is registered on import; [Export] dispatches by name.
//
//	m := metrics.NewMeter()
//	m.Counter("requests").Add(1)
//	_ = metrics.Export("text", m.Collect())
//
// v1 is label-free (instruments are name-keyed); labelled dimensions and the
// Prometheus/OTLP exporters are deferred (ADR 0027).
package metrics

import (
	"io"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	svcmetrics "github.com/kitsunium/sdk/internal/service/metrics"
)

// Meter is the public alias for the instrument factory.
type Meter = coremetrics.Meter

// Counter is the public alias for a monotonic cumulative instrument.
type Counter = coremetrics.Counter

// Gauge is the public alias for an instantaneous up/down instrument.
type Gauge = coremetrics.Gauge

// Histogram is the public alias for a bucketed distribution instrument.
type Histogram = coremetrics.Histogram

// Snapshot is the public alias for a whole-meter point-in-time copy.
type Snapshot = coremetrics.SnapshotValue

// HistogramValue is the public alias for a per-histogram snapshot value.
type HistogramValue = coremetrics.HistogramValue

// Exporter is the public alias for a snapshot shipper.
type Exporter = coremetrics.Exporter

// ExporterName is the public alias for an exporter's registry key.
type ExporterName = coremetrics.ExporterName

var (
	// UnknownExporter is returned by Export when no exporter matches the name.
	UnknownExporter = coremetrics.UnknownExporter
	// ExportFailed wraps an exporter's shipping failure.
	ExportFailed = coremetrics.ExportFailed
	// InstrumentKindConflict is raised when a name is reused across kinds.
	InstrumentKindConflict = coremetrics.InstrumentKindConflict
)

// NewMeter returns a fresh in-memory Meter (Counter/Gauge/Histogram + Collect).
func NewMeter() Meter {
	//: delegate to the service in-memory meter.
	return svcmetrics.NewMeter()
}

// NewTextExporter returns a text Exporter writing to dst under name (not
// auto-registered).
func NewTextExporter(name ExporterName, dst io.Writer) Exporter {
	//: delegate to the service constructor.
	return svcmetrics.NewTextExporter(name, dst)
}

// RegisterExporter adds e to the process-wide exporter registry.
func RegisterExporter(e Exporter) Exporter {
	//: delegate to the core registry.
	return coremetrics.RegisterExporter(e)
}

// Export ships snap through the exporter registered as name.
func Export(name ExporterName, snap Snapshot) error {
	//: delegate to the core dispatch.
	return coremetrics.Export(name, snap)
}

// AvailableExporters returns the sorted list of registered exporter names.
func AvailableExporters() []ExporterName {
	//: delegate to the core registry.
	return coremetrics.AvailableExporters()
}
