// Package metrics provides the in-memory Meter + instruments (Counter, Gauge,
// Histogram) implementing core/metrics, plus a stdlib text Exporter. Instruments
// are atomic and lock-free on the hot path; the Meter serialises only
// instrument creation. v1 is label-free. ADR 0027. Cross-OS: 100% portable.
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
