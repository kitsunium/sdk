// Package metrics — the exportable snapshot: the OTel payload hierarchy in Go.
package metrics

import "time"

// SumMetricValue is every series of ONE sum instrument name, plus the two facts
// the OTel data model attaches to the metric rather than to its points.
//
// A Counter and an UpDownCounter both produce a SumMetricValue; they differ
// only by Monotonic. That is the model's own economy, and it is why the SDK has
// two instrument interfaces and one point shape.
type SumMetricValue struct {
	// Temporality says which window Points cover. Never
	// TemporalityUnspecified — a Meter resolves it at construction.
	Temporality Temporality
	// Monotonic is true for a Counter and false for an UpDownCounter. A
	// backend uses it to decide whether a decrease is a reset or a reading.
	Monotonic bool
	// Description is the instrument's human-readable docstring, or "" when
	// nobody described it. It is NON-IDENTIFYING — the OTel data model says so
	// outright — so it never joins the series identity and an exporter that
	// cannot carry it loses nothing but the prose. See Describer.
	Description string
	// Points holds the name's series, sorted by attribute set.
	Points []SumValue
}

// GaugeMetricValue is every series of ONE gauge instrument name.
//
// It carries no Temporality, and that absence is the model, not an omission: a
// gauge is a sampled reading, so there is no window for it to cover and OTLP's
// Gauge message has no temporality field either. The one-field struct exists so
// that all three kinds present the same walk to an exporter — an exporter that
// had to special-case gauges would special-case them in every format.
type GaugeMetricValue struct {
	// Description is the instrument's human-readable docstring, or "" when
	// nobody described it. See SumMetricValue.Description.
	Description string
	// Points holds the name's series, sorted by attribute set.
	Points []GaugeValue
}

// HistogramMetricValue is every series of ONE histogram instrument name, plus
// its temporality.
//
// Exponential (base-2) histograms, which the OTel model also defines, are not
// implemented: they are a different bucketing scheme with their own scale /
// offset / zero-count fields, and adding them is a second point type rather
// than a field on this one. Deferred by ADR 0044 §Deferred.
type HistogramMetricValue struct {
	// Temporality says which window Points cover. Never
	// TemporalityUnspecified.
	Temporality Temporality
	// Description is the instrument's human-readable docstring, or "" when
	// nobody described it. See SumMetricValue.Description.
	Description string
	// Points holds the name's series, sorted by attribute set.
	Points []HistogramValue
}

// SnapshotValue is a point-in-time copy of every series in a Meter, handed to
// an Exporter. It is the OTel payload hierarchy flattened into one Go value:
// Resource once, Scope once, then metrics keyed by name, each carrying its own
// points — ResourceMetrics → ScopeMetrics → Metric → data points, with the two
// single-element levels collapsed because one Meter has exactly one of each.
//
// The shape is deliberately "instrument name -> its metric", not
// "series key -> value": every wire format an exporter targets groups by name
// first (Prometheus emits one TYPE header per name followed by its series; OTLP
// nests data points inside one Metric). An exporter iterating these maps writes
// its header once per key and its points from the slice, with no regrouping
// pass and no composite key to re-parse.
//
// Two properties an exporter may rely on:
//
//   - Each Points slice is SORTED by attribute set, so a snapshot renders
//     byte-identically twice in a row given the same values — which is what
//     makes exporter output diffable and its tests writable without a sort of
//     their own.
//   - Each Attrs slice is sorted by Key and nil for the dimensionless series,
//     so len(Attrs) == 0 is the "no braces" case rather than something to
//     special-case per exporter.
type SnapshotValue struct {
	// Resource identifies the producer, carried once for the whole payload.
	Resource ResourceValue
	// Scope identifies the instrumentation, carried once for the whole
	// payload.
	Scope ScopeValue
	// StartTime opens the window every point covers. Under
	// TemporalityCumulative it is the meter's own start and repeats across
	// collections; under TemporalityDelta it advances to the previous
	// collection's Time.
	//
	// It is one value for the whole snapshot rather than one per point
	// because every point in one Collect covers the same window. An encoder
	// that needs it per point (OTLP does) copies this one down.
	StartTime time.Time
	// Time closes the window: the instant the collection was taken.
	Time time.Time
	// Sums maps an instrument name to its sum metric — Counters and
	// UpDownCounters together, told apart by SumMetricValue.Monotonic.
	Sums map[string]SumMetricValue
	// Gauges maps an instrument name to its gauge metric.
	Gauges map[string]GaugeMetricValue
	// Histograms maps an instrument name to its histogram metric.
	Histograms map[string]HistogramMetricValue
}
