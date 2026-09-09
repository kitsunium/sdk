// Package metrics — the exportable histogram point: an explicit-bucket
// distribution.
package metrics

// HistogramValue is one series of a histogram metric: the attribute set that
// identifies it, the explicit bucket upper bounds, the count per bucket
// (len == len(Bounds)+1, the last being the +Inf overflow), and the Sum/Count
// of every observation in the window.
type HistogramValue struct {
	// Attrs is the series' attribute set, sorted by Key; nil for the
	// dimensionless series. See SumValue.Attrs for the aliasing rule.
	Attrs []AttrValue
	// Bounds holds the histogram's explicit upper bounds, ascending. OTLP
	// calls this field explicit_bounds.
	Bounds []float64
	// Counts holds one count per bucket plus the +Inf overflow slot. The
	// counts are PER BUCKET, not cumulative — a format that wants a
	// cumulative ladder (Prometheus) builds it as it walks.
	Counts []uint64
	// Sum is the total of every observed value in the window.
	Sum float64
	// Count is how many observations the window holds.
	Count uint64
}
