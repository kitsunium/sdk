// Package metrics — the Meter (instrument factory + collector).
package metrics

// Meter mints named instruments and collects their values. Repeated calls with
// the same name return the SAME instrument (idempotent); a name reused across
// kinds is a programmer error. Implementations MUST be safe for concurrent use.
type Meter interface {
	Counter(name string) Counter
	Gauge(name string) Gauge
	Histogram(name string, buckets []float64) Histogram
	// Collect returns a point-in-time SnapshotValue of every instrument.
	Collect() SnapshotValue
}
