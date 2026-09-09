// Package metrics provides the in-memory Meter + lock-free instruments
// (Counter, UpDownCounter, Gauge, Histogram and their observable counterparts)
// implementing core/metrics, plus two stdlib Exporters. Instruments are atomic
// and lock-free on the hot path; the Meter takes only a read lock to resolve an
// existing series and serialises creation alone. A series is one instrument
// name plus one TYPED attribute set, with a per-name cardinality bound that
// folds the excess into one aggregated overflow series. ADR 0027 / ADR 0044.
// Cross-OS: 100% portable.
package metrics

import "sync/atomic"

// memSum is the storage behind BOTH counter kinds: an atomic int64 total plus
// the two flags that decide what may be written to it and how a delta window is
// computed from it.
//
// One type rather than two because the OTel data model has one Sum point and
// puts monotonicity on the metric. The flags are written once at creation and
// never mutated, so reading them on the hot path is a predictable branch on an
// immutable field, not synchronisation.
type memSum struct {
	v atomic.Int64
	// previous is the last ABSOLUTE value an observable callback reported.
	// Touched only from Collect, which memMeter serialises with collectMu,
	// so it needs no atomic. Unused by a synchronous sum.
	previous int64
	// monotonic refuses a non-positive Add, which is what makes a Counter a
	// Counter. An UpDownCounter clears it.
	monotonic bool
	// observed marks a series whose value is written by a callback at
	// collection time rather than accumulated by the caller.
	observed bool
}

// Add adjusts the total by delta.
//
// On a MONOTONIC sum a non-positive delta is ignored: a Counter that could go
// down is an UpDownCounter, and a backend reading Monotonic = true is entitled
// to treat a decrease as a process restart. That is also why Dec below is inert
// on a Counter reached through a type assertion rather than a compile error —
// the interface a Counter is handed out behind does not carry Dec at all.
func (s *memSum) Add(delta int64) {
	//: a monotonic sum never decreases.
	if s.monotonic && delta <= 0 {
		//: nothing to record.
		return
	}
	//: atomic increment on the hot path.
	s.v.Add(delta)
}

// Inc adds one, which is a valid step for both monotonicities.
func (s *memSum) Inc() {
	//: one is always a legal increment.
	s.v.Add(1)
}

// Dec subtracts one. It is inert on a monotonic sum, for the reason Add gives.
func (s *memSum) Dec() {
	//: routed through Add so the monotonicity rule lives in exactly one place.
	s.Add(-1)
}

// observe records the ABSOLUTE value a callback reported, converting it to the
// meter's temporality.
//
// Under cumulative temporality the absolute value IS the report. Under delta it
// is not: an observable states a running total (goroutines allocated since
// boot), so the window is the difference from the previous observation, and
// remembering that previous value is the only bookkeeping an asynchronous
// instrument needs that a synchronous one does not.
//
// Caller holds memMeter.collectMu, which is what makes previous safe to touch
// without an atomic.
func (s *memSum) observe(absolute int64, delta bool) {
	//: a cumulative reader wants the total exactly as reported.
	if !delta {
		//: publish the absolute value.
		s.v.Store(absolute)
		//: nothing else to remember.
		return
	}
	//: a delta reader wants the window since the previous observation.
	s.v.Store(absolute - s.previous)
	//: the next window starts here.
	s.previous = absolute
}

// collect reads the value for a snapshot, consuming the window when the meter
// is a delta reader.
//
// An OBSERVED series is never swapped to zero: observe already stored the
// window, and swapping would report the same delta twice — once as itself and
// once as its own negation on the next collection.
func (s *memSum) collect(delta bool) int64 {
	//: a synchronous series under delta hands over its accumulation.
	if delta && !s.observed {
		//: read and reset in one atomic step, so no observation is lost
		//: between the two.
		return s.v.Swap(0)
	}
	//: cumulative, or an observable that already holds the right number.
	return s.v.Load()
}

// newCounter builds a zeroed monotonic sum.
func newCounter() *memSum {
	//: a counter starts at zero and refuses to go below it.
	return &memSum{monotonic: true}
}

// newUpDownCounter builds a zeroed non-monotonic sum.
func newUpDownCounter() *memSum {
	//: an up-down counter starts at zero and accepts any delta.
	return &memSum{}
}

// newObservableCounter builds a monotonic sum written by a callback.
func newObservableCounter() *memSum {
	//: monotonic, and read rather than written by the caller.
	return &memSum{monotonic: true, observed: true}
}

// newObservableUpDownCounter builds a non-monotonic sum written by a callback.
func newObservableUpDownCounter() *memSum {
	//: non-monotonic, and read rather than written by the caller.
	return &memSum{observed: true}
}
