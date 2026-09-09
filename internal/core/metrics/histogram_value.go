// Package metrics — the exportable snapshot value types.
package metrics

// HistogramValue is a point-in-time copy of ONE histogram series: the label set
// that identifies it, the bucket upper bounds, the cumulative count per bucket
// (len == len(Buckets)+1, the last being the +Inf overflow), and the running
// Sum/Count of all observations.
type HistogramValue struct {
	// Labels is the series' label set, sorted by Key; nil for the
	// dimensionless series. See CounterValue.Labels for the aliasing rule.
	Labels []LabelValue
	// Buckets holds the histogram's upper bounds, ascending.
	Buckets []float64
	// Counts holds one count per bucket plus the +Inf overflow slot.
	Counts []uint64
	// Sum is the running total of every observed value.
	Sum float64
	// Count is how many observations the histogram has taken.
	Count uint64
}

// CounterValue is a point-in-time copy of ONE counter series: the label set
// that identifies it plus its cumulative total.
type CounterValue struct {
	// Labels is the series' label set, sorted by Key; nil for the
	// dimensionless series.
	//
	// The slice ALIASES the meter's own copy — it is not cloned per Collect.
	// The meter never mutates a label set after the series is created, so a
	// reader is safe; a writer would corrupt every future snapshot. Cloning
	// instead would cost one allocation per series per collection, i.e. it
	// would scale the cost of scraping with cardinality, which is the exact
	// axis this whole feature exists to bound.
	Labels []LabelValue
	// Value is the counter's cumulative total.
	Value int64
}

// GaugeValue is a point-in-time copy of ONE gauge series: the label set that
// identifies it plus its instantaneous reading.
type GaugeValue struct {
	// Labels is the series' label set, sorted by Key; nil for the
	// dimensionless series. See CounterValue.Labels for the aliasing rule.
	Labels []LabelValue
	// Value is the gauge's instantaneous reading.
	Value float64
}

// SnapshotValue is a point-in-time copy of every series in a Meter, handed to
// an Exporter.
//
// The shape is deliberately "instrument name -> its series", not
// "series key -> value": every wire format an exporter targets groups by name
// first (Prometheus emits one HELP/TYPE header per name followed by its
// series; OTLP nests data points inside a Metric). An exporter iterating this
// map writes its header once per key and its points from the slice, with no
// regrouping pass and no parsing of a composite key.
//
// Each slice is sorted by label set, so a snapshot renders byte-identically
// twice in a row given the same values — which is what makes exporter output
// diffable and its tests writable without a sorting step of their own.
type SnapshotValue struct {
	// Counters maps an instrument name to its counter series.
	Counters map[string][]CounterValue
	// Gauges maps an instrument name to its gauge series.
	Gauges map[string][]GaugeValue
	// Histograms maps an instrument name to its histogram series.
	Histograms map[string][]HistogramValue
}
