// Package metrics — the exportable snapshot value types.
package metrics

// HistogramValue is a point-in-time copy of a histogram: the bucket upper
// bounds, the cumulative count per bucket (len == len(Buckets)+1, the last being
// the +Inf overflow), and the running Sum/Count of all observations.
type HistogramValue struct {
	Buckets []float64
	Counts  []uint64
	Sum     float64
	Count   uint64
}

// SnapshotValue is a point-in-time copy of every instrument in a Meter, handed
// to an Exporter. Maps are keyed by instrument name.
type SnapshotValue struct {
	Counters   map[string]int64
	Gauges     map[string]float64
	Histograms map[string]HistogramValue
}
