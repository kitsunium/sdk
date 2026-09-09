// Package metrics provides the in-memory Meter + instruments (Counter, Gauge,
// Histogram) implementing core/metrics, plus a stdlib text Exporter.
// Instruments are atomic and lock-free on the hot path; the Meter takes only a
// read lock to resolve an existing series and serialises creation alone.
// Instruments are keyed by name AND label set, with a per-name cardinality
// bound that folds the excess into one aggregated overflow series. ADR 0027.
// Cross-OS: 100% portable.
package metrics

import "sync/atomic"

// memCounter is an atomic monotonic counter.
type memCounter struct {
	v atomic.Int64
}

// Add increments the counter; a non-positive delta is ignored (counters are
// monotonic).
func (c *memCounter) Add(delta int64) {
	//: a counter never decreases — ignore non-positive deltas.
	if delta > 0 {
		//: atomic increment on the hot path.
		c.v.Add(delta)
	}
}

// load reads the current value for Collect.
func (c *memCounter) load() int64 {
	//: lock-free read of the cumulative total.
	return c.v.Load()
}

// Inc increments the counter by one.
func (c *memCounter) Inc() {
	//: one is always a valid monotonic step.
	c.v.Add(1)
}

// newMemCounter builds a zeroed counter. A package-level function value so the
// Meter's create path passes it without allocating a closure.
func newMemCounter() *memCounter {
	//: a counter starts at zero and needs no configuration.
	return &memCounter{}
}
