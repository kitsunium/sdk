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
// puts monotonicity on the metric. The two flags are written once at creation
// and never mutated, so reading them on the hot path is a predictable branch on
// an immutable field, not synchronisation. previous and reports are the
// opposite: collection-time bookkeeping for an observed series, mutated only
// under collectMu.
type memSum struct {
	v atomic.Int64
	// previous is the last ABSOLUTE value an observable callback reported.
	// Touched only from Collect, which memMeter serialises with collectMu,
	// so it needs no atomic. Unused by a synchronous sum.
	previous int64
	// reports counts the callback's reports of an observed series during the
	// collection in progress: observe adds one, a delta collect reads and
	// clears it. Zero at collection time is a series the callback did not
	// mention, whose window is therefore empty — v still holds the last
	// window, and is never swapped. Guarded by collectMu exactly as previous
	// is. Unused by a synchronous sum.
	reports uint32
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
// boot), so the window is the difference from the previous observation.
// Remembering that previous value, and counting that this collection reported
// the series at all, is the bookkeeping an asynchronous instrument needs that a
// synchronous one does not.
//
// A MONOTONIC total that went down is read as a RESET, and the window is the
// new absolute: whatever restarted behind the callback (a process restart
// zeroes its counters) has counted exactly that much since. Differencing it
// instead would put a negative delta on a sum whose metric says Monotonic,
// which no reader of a monotonic sum is prepared to receive. An up-down total
// is a signed quantity, so its decrease stays a signed window. The cumulative
// path publishes a decrease as reported, and a cumulative reader applies the
// same reading of a drop itself.
//
// Caller holds memMeter.collectMu, which is what makes previous and reports
// safe to touch without an atomic.
func (s *memSum) observe(absolute int64, delta bool) {
	//: a cumulative reader wants the total exactly as reported.
	if !delta {
		//: publish the absolute value.
		s.v.Store(absolute)
		//: nothing else to remember.
		return
	}
	//: a delta reader wants the window since the previous observation.
	window := absolute - s.previous
	//: a monotonic total only falls when it restarted from zero.
	if s.monotonic && absolute < s.previous {
		//: everything counted since the reset.
		window = absolute
	}
	//: publish the window.
	s.v.Store(window)
	//: the next window starts here.
	s.previous = absolute
	//: and the collection in progress reported this series once more.
	s.reports++
}

// collect reads the value for a snapshot, consuming the window when the meter
// is a delta reader.
//
// An OBSERVED series is never swapped to zero, because v is not its
// accumulator: observe OVERWRITES v with the whole window each time the
// callback reports the series, and derives that window from previous, never
// from v. What a delta read consumes for such a series is the REPORT, which the
// reports field counts. A series its callback did not report in this
// collection saw nothing, so it reads zero — exactly what an untouched
// synchronous series reads — rather than the window stored for some EARLIER
// collection, which a backend summing deltas would otherwise count again on
// every scrape until the series came back. previous is left alone, so a series
// that comes back is differenced against its last reading.
func (s *memSum) collect(delta bool) int64 {
	//: a cumulative reader reads every series, observed or not, as it stands.
	if !delta {
		//: the running total, or the last total a callback reported.
		return s.v.Load()
	}
	//: a synchronous series under delta hands over its accumulation.
	if !s.observed {
		//: read and reset in one atomic step, so no observation is lost
		//: between the two.
		return s.v.Swap(0)
	}
	//: an observable its callback did not report in this collection.
	if s.reports == 0 {
		//: an empty window.
		return 0
	}
	//: consume the reports, so the next collection needs fresh ones.
	s.reports = 0
	//: the window observe stored in this collection.
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
