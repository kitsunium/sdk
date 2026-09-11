//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/metrics .

// Package metrics is the public facade for the SDK's observability domain — the
// natural twin of the logger. A [Meter] (from [NewMeter]) mints lock-free
// [Counter]/[Gauge]/[Histogram] instruments; [Meter.Collect] takes a [Snapshot]
// that an [Exporter] ships out. Two stdlib exporters are registered on import —
// "text" (a one-line-per-series diagnostic) and "prometheus" (the Prometheus
// text exposition format) — and [Export] dispatches by name. Both write to
// stderr so that importing this package never arms a writer on stdout, which a
// process may be using as a protocol channel (ADR 0030); pass os.Stdout to
// [NewTextExporter] to opt in explicitly.
//
//	m := metrics.NewMeter()
//	m.Counter("requests").Add(1)
//	_ = metrics.Export("text", m.Collect())
//
// # Scraping
//
// The registered "prometheus" exporter is a diagnostic; a scrape endpoint binds
// its own with [NewPrometheusExporter] and hands it the response writer:
//
//	http.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
//	    w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
//	    _ = metrics.NewPrometheusExporter("scrape", w).Export(m.Collect())
//	})
//
// It writes one "# TYPE" header per instrument name followed by that name's
// series, renders a histogram as the cumulative _bucket ladder (including the
// mandatory le="+Inf" line) plus _sum and _count, and escapes every label value
// so a value carrying a quote or a newline cannot forge a line a reader parses
// as another series.
//
// It REFUSES, rather than rewrites, a name the format cannot spell: a metric
// name must match [a-zA-Z_:][a-zA-Z0-9_:]* and a label name
// [a-zA-Z_][a-zA-Z0-9_]*. An instrument name is written at the call site and
// constant for the process, so a rejected one is rejected on the first scrape
// or never — whereas mapping the offending characters to "_" would silently
// merge two distinct instruments into one metric family. The refusal is typed
// ([InvalidMetricName], [InvalidLabelName], [ReservedLabelName]) and leaves the
// writer untouched, because a truncated exposition parses as a complete one.
//
// # Labels and series
//
// An instrument is identified by its name AND its labels. One name plus one
// label set is one SERIES, and every fetch of that pair returns the same
// instrument, so observations accumulate in one place wherever they are made:
//
//	m.Counter("requests", metrics.Label{Key: "method", Value: "GET"}).Inc()
//
// Label order does not matter — a label set is a set. Passing no labels names
// the dimensionless series, which is exactly what a call without labels always
// meant. A label KEY is structure: it is written at the call site and constant
// for the process, so an empty or repeated key is a programmer error and
// panics with [InvalidLabel]. A label VALUE is data and may be anything.
//
// # Cardinality
//
// Distinct label values create distinct series, and an unbounded stream of
// them is a memory incident rather than a reporting inconvenience. Every Meter
// therefore bounds how many series ONE instrument name may hold —
// [DefaultMaxSeriesPerInstrument] unless [NewMeterWithConfig] says otherwise.
// Past the bound, further label sets are folded into a single aggregated
// series carrying the label [OverflowLabelKey]="true": memory stays bounded,
// no observation is dropped, and the condition is visible in every snapshot
// from then on. What is lost is the breakdown — once folded, an observation's
// own labels are gone.
//
// A non-positive MaxSeriesPerInstrument clamps to the default. There is no
// setting that means "unbounded" (ADR 0031); a caller who wants a very large
// bound writes a very large number, where a reviewer can see it.
//
// A [Snapshot] maps each instrument name to its series, which is the shape
// every per-series wire format wants — and the shape the Prometheus exporter
// above consumes without a regrouping pass. The OTLP exporter remains deferred
// to a third-party package (ADR 0027); the Prometheus protobuf format does too.
package metrics

import (
	"io"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	svcmetrics "github.com/kitsunium/sdk/internal/service/metrics"
)

const (
	// DefaultMaxSeriesPerInstrument is the cardinality bound NewMeter applies,
	// and the value a non-positive MeterConfig knob clamps to.
	DefaultMaxSeriesPerInstrument int = svcmetrics.DefaultMaxSeriesPerInstrument
	// OverflowLabelKey is the reserved label naming a meter's aggregated
	// overflow series. It appears in a snapshot only once an instrument has
	// exceeded its cardinality bound.
	OverflowLabelKey string = coremetrics.OverflowLabelKey
	// OverflowLabelValue is the only value ever stored under OverflowLabelKey.
	OverflowLabelValue string = coremetrics.OverflowLabelValue
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

// Label is the public alias for one dimension of a series.
type Label = coremetrics.LabelValue

// CounterValue is the public alias for a per-series counter snapshot value.
type CounterValue = coremetrics.CounterValue

// GaugeValue is the public alias for a per-series gauge snapshot value.
type GaugeValue = coremetrics.GaugeValue

// HistogramValue is the public alias for a per-series histogram snapshot value.
type HistogramValue = coremetrics.HistogramValue

// MeterConfig is the public alias for a Meter's cardinality configuration.
type MeterConfig = svcmetrics.MeterConfig

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
	// InvalidLabel is raised when a label set has an empty or repeated key.
	InvalidLabel = coremetrics.InvalidLabel
	// InvalidMetricName is returned by the Prometheus exporter when an
	// instrument name is not a valid Prometheus metric name.
	InvalidMetricName = svcmetrics.InvalidMetricName
	// InvalidLabelName is returned by the Prometheus exporter when a label key
	// is not a valid Prometheus label name.
	InvalidLabelName = svcmetrics.InvalidLabelName
	// ReservedLabelName is returned by the Prometheus exporter when a label key
	// is legal but reserved — a "__" prefix, or "le" on a histogram.
	ReservedLabelName = svcmetrics.ReservedLabelName
)

// NewMeter returns a fresh in-memory Meter (Counter/Gauge/Histogram + Collect)
// bounded at DefaultMaxSeriesPerInstrument series per instrument name.
func NewMeter() Meter {
	//: delegate to the service in-memory meter.
	return svcmetrics.NewMeter()
}

// NewMeterWithConfig returns a fresh in-memory Meter honouring cfg. A
// non-positive MaxSeriesPerInstrument clamps to
// DefaultMaxSeriesPerInstrument — it never means unbounded.
func NewMeterWithConfig(cfg MeterConfig) Meter {
	//: delegate to the service in-memory meter.
	return svcmetrics.NewMeterWithConfig(cfg)
}

// NewTextExporter returns a text Exporter writing to dst under name (not
// auto-registered).
func NewTextExporter(name ExporterName, dst io.Writer) Exporter {
	//: delegate to the service constructor.
	return svcmetrics.NewTextExporter(name, dst)
}

// NewPrometheusExporter returns an Exporter rendering the Prometheus text
// exposition format to dst under name (not auto-registered). Hand it the
// http.ResponseWriter of a /metrics handler; the registered "prometheus"
// exporter targets stderr and is a diagnostic, not a scrape endpoint.
func NewPrometheusExporter(name ExporterName, dst io.Writer) Exporter {
	//: delegate to the service constructor.
	return svcmetrics.NewPrometheusExporter(name, dst)
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
