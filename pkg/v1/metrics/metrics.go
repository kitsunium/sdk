//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/metrics .

// Package metrics is the public facade for the SDK's observability domain — the
// natural twin of the logger, shaped on the OpenTelemetry metrics DATA MODEL.
//
// The model is OpenTelemetry's; the code is not. Nothing here imports
// go.opentelemetry.io: OTel is a published specification, and this SDK
// implements it the way it implements the Prometheus exposition format or
// RFC 7517 — from the document, with the standard library. What you get is the
// model's vocabulary (typed attributes, aggregation temporality, Resource,
// InstrumentationScope, monotonic and non-monotonic sums, observable
// instruments) with this SDK's dependency budget, which is zero.
//
//	m := metrics.NewMeter()
//	m.Counter("requests").Add(1)
//	_ = metrics.Export("text", m.Collect())
//
// # Instruments
//
// A [Meter] mints four synchronous instruments and registers three
// asynchronous ones:
//
//   - [Counter] — a MONOTONIC sum. Requests served, bytes written. A
//     non-positive Add is ignored, because a backend reading a monotonic sum
//     is entitled to treat a decrease as a process restart.
//   - [UpDownCounter] — a NON-MONOTONIC sum. In-flight requests, queue depth.
//     Additive like a counter, but Add may be negative and Dec exists.
//   - [Gauge] — a sampled reading with no arithmetic behind it. Temperature,
//     a configured limit.
//   - [Histogram] — a bucketed distribution. Latency, payload size.
//   - [FullMeter.ObservableCounter], [FullMeter.ObservableUpDownCounter] and
//     [FullMeter.ObservableGauge] — a callback read once per [Meter.Collect],
//     for a value that already exists somewhere and only needs reading
//     (runtime.NumGoroutine(), a cache size). The callback reports the
//     ABSOLUTE value; the SDK differences it when the meter is a delta reader.
//
// A Counter and an UpDownCounter both produce a [SumMetric] in the snapshot,
// told apart by [SumMetric.Monotonic]. That is the OTel data model's own
// economy: monotonicity is a FIELD of a sum, not a second point type.
//
// [Meter] itself carries only the three instruments and Collect it shipped
// with; UpDownCounter and the observables live on the sibling interfaces
// [UpDownMeter] and [AsyncMeter], because a published Go interface cannot grow
// a method without breaking every downstream implementer (ADR 0039).
// [FullMeter] is the union, and it is what [NewMeter] returns.
//
// # Attributes
//
// An instrument is identified by its name AND its attributes. One name plus one
// attribute set is one SERIES, and every fetch of that pair returns the same
// instrument, so observations accumulate in one place wherever they are made.
// An attribute's value is TYPED — string, bool, int64 or float64 — and built by
// one of four constructors:
//
//	m.Counter("requests",
//	    metrics.String("http.request.method", "GET"),
//	    metrics.Int64("http.response.status_code", 503),
//	    metrics.Bool("cache.hit", false),
//	).Inc()
//
// The type is part of the identity: String("v", "1") and Int64("v", 1) are two
// different series, not one. Attribute ORDER is not — a set is a set. Passing
// no attributes names the dimensionless series.
//
// An attribute KEY is structure: it is written at the call site and constant
// for the process, so an empty key, a repeated key, or a value built by a
// struct literal instead of a constructor is a programmer error and panics with
// [InvalidAttribute]. An attribute VALUE is data and may be anything.
//
// Homogeneous ARRAY attributes, which the OTel model also allows, are not
// implemented — see the package's CLAUDE.md for what they would cost on the
// observation path.
//
// # Temporality
//
// [Temporality] says which window a reported number covers, and it is the one
// fact a metric value cannot carry by itself:
//
//   - [TemporalityCumulative] — the point covers everything since the meter
//     started. Successive collections repeat the start timestamp. This is what
//     an unconfigured [Meter] does, because an in-memory meter accumulates into
//     atomics and never resets them.
//   - [TemporalityDelta] — the point covers only the window since the previous
//     collection. [Meter.Collect] then CONSUMES what it reports, so a delta
//     meter has exactly one reader.
//
// [TemporalityUnspecified] is the zero value and resolves to cumulative; there
// is no setting that leaves it undecided (ADR 0031).
//
// # Resource and scope
//
// A [Snapshot] carries two identities once for the whole payload rather than on
// every point: the [Resource] (who produced this — service.name, and anything
// else the caller adds) and the [Scope] (what instrumented it — a library name
// and version). An absent service.name resolves to [UnknownService], which is
// what the specification mandates rather than a value this SDK invented.
//
//	m := metrics.NewMeterWithConfig(metrics.MeterConfig{
//	    Resource: metrics.Resource{Attrs: []metrics.Attr{
//	        metrics.String(metrics.ServiceNameKey, "orders"),
//	    }},
//	    Scope: metrics.Scope{Name: "github.com/acme/orders", Version: "1.4.0"},
//	})
//
// # Cardinality
//
// Distinct attribute values create distinct series, and an unbounded stream of
// them is a memory incident rather than a reporting inconvenience. Every Meter
// therefore bounds how many series ONE instrument name may hold —
// [DefaultMaxSeriesPerInstrument] unless [NewMeterWithConfig] says otherwise.
// Past the bound, further attribute sets are folded into a single aggregated
// series carrying [OverflowAttrKey]=true: memory stays bounded, no observation
// is dropped, and the condition is visible in every snapshot from then on. What
// is lost is the breakdown — once folded, an observation's own attributes are
// gone.
//
// A non-positive MaxSeriesPerInstrument clamps to the default. There is no
// setting that means "unbounded" (ADR 0031); a caller who wants a very large
// bound writes a very large number, where a reviewer can see it.
//
// # Exporting
//
// A [Snapshot] maps each instrument name to its metric, and each metric to its
// series — the shape every per-series wire format wants, and the shape an
// OTLP encoder can walk without a regrouping pass. Two stdlib exporters are
// registered on import and [Export] dispatches by name. Both write to stderr so
// that importing this package never arms a writer on stdout, which a process
// may be using as a protocol channel (ADR 0030).
//
// "text" is a diagnostic that prints the whole model — resource, scope, window,
// temporality, monotonicity, and each attribute with its type visible.
//
// "prometheus" is a deliberately LOSSY connector to the Prometheus text
// exposition format, kept because a Prometheus deployment is a real
// destination and not because the format can carry this model. What it loses:
//
//   - Temporality. The format has none, and a server reads every counter as
//     cumulative. A delta snapshot is REFUSED ([UnsupportedTemporality]) rather
//     than mis-labelled.
//   - The attribute's TYPE. A Prometheus label value is a string, so
//     Int64("v", 1) and String("v", "1") — two series here — become one there.
//   - The Resource and the Scope, which have nowhere to go. In particular
//     service.name cannot even be spelled: a Prometheus label name is
//     [a-zA-Z_][a-zA-Z0-9_]* and the dot is outside it.
//   - Exemplars, which this SDK does not produce at all.
//
// It REFUSES, rather than rewrites, a name the format cannot spell: an
// instrument name must match [a-zA-Z_:][a-zA-Z0-9_:]* and an attribute key
// [a-zA-Z_][a-zA-Z0-9_]*, which means an OTel-conventional dotted key is
// refused too. Mapping the offending characters to "_" would silently merge two
// distinct instruments into one family, and a metrics pipeline cannot detect
// that afterwards. The refusal is typed ([InvalidMetricName],
// [InvalidLabelName], [ReservedLabelName]) and leaves the writer untouched,
// because a truncated exposition parses as a complete one.
//
// A scrape endpoint binds its own exporter with [NewPrometheusExporter] and
// hands it the response writer:
//
//	http.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
//	    w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
//	    _ = metrics.NewPrometheusExporter("scrape", w).Export(m.Collect())
//	})
//
// A non-monotonic sum is typed `gauge` there, not `counter`, because `rate()`
// on a counter re-extrapolates from zero every time the value falls.
//
// An OTLP exporter is not in this package; the snapshot shape exists so that
// one can be written without reconstructing anything.
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
	// OverflowAttrKey is the reserved attribute naming a meter's aggregated
	// overflow series. It appears in a snapshot only once an instrument has
	// exceeded its cardinality bound, and its value is the boolean true.
	OverflowAttrKey string = coremetrics.OverflowAttrKey
	// ServiceNameKey is the attribute key the OTel resource semantic
	// conventions reserve for the logical name of the service.
	ServiceNameKey string = coremetrics.ServiceNameKey
	// UnknownService is the service.name the specification mandates when a
	// producer supplies none.
	UnknownService string = coremetrics.UnknownService
	// DefaultScopeName names this SDK as the instrumenting library when a
	// caller declares no scope of their own.
	DefaultScopeName string = coremetrics.DefaultScopeName
)

// Meter is the public alias for the frozen instrument factory: Counter, Gauge,
// Histogram and Collect.
type Meter = coremetrics.Meter

// UpDownMeter is the public alias for the sibling port that mints the
// non-monotonic sum Meter cannot.
type UpDownMeter = coremetrics.UpDownMeter

// AsyncMeter is the public alias for the sibling port that registers
// observable (asynchronous) instruments.
type AsyncMeter = coremetrics.AsyncMeter

// FullMeter is the public alias for the union of all three — what NewMeter
// returns.
type FullMeter = coremetrics.FullMeter

// Counter is the public alias for a monotonic sum instrument.
type Counter = coremetrics.Counter

// UpDownCounter is the public alias for a non-monotonic sum instrument.
type UpDownCounter = coremetrics.UpDownCounter

// Gauge is the public alias for a sampled-reading instrument.
type Gauge = coremetrics.Gauge

// Histogram is the public alias for a bucketed distribution instrument.
type Histogram = coremetrics.Histogram

// Attr is the public alias for one typed dimension of a series.
type Attr = coremetrics.AttrValue

// AttrKind is the public alias for an attribute value's type tag.
type AttrKind = coremetrics.AttrKind

// AttrKindInvalid marks an attribute whose value no constructor ever set.
const AttrKindInvalid AttrKind = coremetrics.AttrKindInvalid

// AttrKindString marks a string-valued attribute.
const AttrKindString AttrKind = coremetrics.AttrKindString

// AttrKindBool marks a bool-valued attribute.
const AttrKindBool AttrKind = coremetrics.AttrKindBool

// AttrKindInt64 marks a signed 64-bit integer attribute.
const AttrKindInt64 AttrKind = coremetrics.AttrKindInt64

// AttrKindFloat64 marks an IEEE-754 double attribute.
const AttrKindFloat64 AttrKind = coremetrics.AttrKindFloat64

// Temporality is the public alias for a metric's aggregation temporality.
type Temporality = coremetrics.Temporality

// TemporalityUnspecified is the unset zero value; it resolves to
// TemporalityCumulative and never reaches a Snapshot.
const TemporalityUnspecified Temporality = coremetrics.TemporalityUnspecified

// TemporalityDelta reports the window since the previous collection.
const TemporalityDelta Temporality = coremetrics.TemporalityDelta

// TemporalityCumulative reports the window since the series started.
const TemporalityCumulative Temporality = coremetrics.TemporalityCumulative

// Resource is the public alias for the producer's identity, carried once per
// snapshot.
type Resource = coremetrics.ResourceValue

// Scope is the public alias for the instrumentation's identity, carried once
// per snapshot.
type Scope = coremetrics.ScopeValue

// Snapshot is the public alias for a whole-meter point-in-time copy.
type Snapshot = coremetrics.SnapshotValue

// SumMetric is the public alias for every series of one sum instrument name,
// plus its temporality and monotonicity.
type SumMetric = coremetrics.SumMetricValue

// SumPoint is the public alias for one sum series.
type SumPoint = coremetrics.SumValue

// GaugeMetric is the public alias for every series of one gauge instrument name.
type GaugeMetric = coremetrics.GaugeMetricValue

// GaugePoint is the public alias for one gauge series.
type GaugePoint = coremetrics.GaugeValue

// HistogramMetric is the public alias for every series of one histogram
// instrument name, plus its temporality.
type HistogramMetric = coremetrics.HistogramMetricValue

// HistogramPoint is the public alias for one histogram series.
type HistogramPoint = coremetrics.HistogramValue

// ObserveInt64 is the public alias for the reporting function an integer
// observable callback is handed.
type ObserveInt64 = coremetrics.ObserveInt64

// ObserveFloat64 is the public alias for the reporting function a double
// observable callback is handed.
type ObserveFloat64 = coremetrics.ObserveFloat64

// Int64Callback is the public alias for an integer observable's callback.
type Int64Callback = coremetrics.Int64Callback

// Float64Callback is the public alias for a double observable's callback.
type Float64Callback = coremetrics.Float64Callback

// MeterConfig is the public alias for a Meter's configuration.
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
	// InvalidAttribute is raised when an attribute set has an empty or
	// repeated key, or a value no constructor ever set.
	InvalidAttribute = coremetrics.InvalidAttribute
	// InvalidTemporality is raised when a MeterConfig carries a Temporality
	// that is none of the three constants.
	InvalidTemporality = coremetrics.InvalidTemporality
	// InvalidMetricName is returned by the Prometheus connector when an
	// instrument name is not a valid Prometheus metric name.
	InvalidMetricName = svcmetrics.InvalidMetricName
	// InvalidLabelName is returned by the Prometheus connector when an
	// attribute key is not a valid Prometheus label name.
	InvalidLabelName = svcmetrics.InvalidLabelName
	// ReservedLabelName is returned by the Prometheus connector when an
	// attribute key is legal but reserved — a "__" prefix, or "le" on a
	// histogram.
	ReservedLabelName = svcmetrics.ReservedLabelName
	// UnsupportedTemporality is returned by the Prometheus connector when the
	// snapshot is a delta one, which the exposition format cannot express.
	UnsupportedTemporality = svcmetrics.UnsupportedTemporality
)

// String returns a string-valued attribute. It is the shortest of the four
// constructors to write because a string dimension is the common one.
func String(key, value string) Attr {
	//: delegate to the core constructor.
	return coremetrics.String(key, value)
}

// Bool returns a bool-valued attribute.
func Bool(key string, value bool) Attr {
	//: delegate to the core constructor.
	return coremetrics.Bool(key, value)
}

// Int64 returns a signed-integer attribute.
func Int64(key string, value int64) Attr {
	//: delegate to the core constructor.
	return coremetrics.Int64(key, value)
}

// Float64 returns a double attribute. A float is a measurement rather than a
// dimension, and it is keyed on its bit pattern — see the core documentation.
func Float64(key string, value float64) Attr {
	//: delegate to the core constructor.
	return coremetrics.Float64(key, value)
}

// NewMeter returns a fresh in-memory Meter with every MeterConfig knob at its
// resolved default: bounded at DefaultMaxSeriesPerInstrument, cumulative,
// service.name=unknown_service, this SDK as the scope, and the system clock.
func NewMeter() FullMeter {
	//: delegate to the service in-memory meter.
	return svcmetrics.NewMeter()
}

// NewMeterWithConfig returns a fresh in-memory Meter honouring cfg. Every unset
// field resolves to a working value and none of them to an inert one.
func NewMeterWithConfig(cfg MeterConfig) FullMeter {
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
