// Package metrics — the Histogram instrument.
package metrics

import "time"

// Histogram records a distribution of observations into fixed buckets.
type Histogram interface {
	// Record observes one value, incrementing its bucket and the sum/count.
	Record(value float64)
	// RecordDuration observes d as seconds (latency-histogram convenience).
	RecordDuration(d time.Duration)
}
