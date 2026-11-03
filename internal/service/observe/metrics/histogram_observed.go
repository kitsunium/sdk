package metrics

import (
	"math"
	"slices"
	"sync/atomic"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
)

// observedState is what an observed histogram series keeps between
// readings. Touched only from Collect, which memMeter serialises with
// collectMu.
type observedState struct {
	// previous and previousSum are the last ABSOLUTE reading, which a delta
	// meter differences the next one against.
	previous    []uint64
	previousSum float64
	// noSum is the last reading's NoSum: the source keeps no total.
	noSum bool
	// reported is whether the callback reported the series during the
	// collection in progress; a delta collection of an unreported series is
	// an empty window.
	reported bool
}

// newObservedHistogram builds a histogram series an ObservableHistogram
// callback writes; its layout is the first reading's.
func newObservedHistogram() *memHistogram {
	//: no layout until the callback reports one.
	return &memHistogram{observed: &observedState{}}
}

// observe takes one ABSOLUTE reading of the series: its layout, its counts,
// its sum. A cumulative meter keeps the reading as it is; a delta meter
// keeps its difference from the previous one, bucket by bucket, and takes a
// reading whose layout changed or whose counts went down as a restart. It
// runs inside Collect, which collectMu serialises, so the plain fields need
// no atomics; the bucket counts stay atomic because snapshot reads them as
// a synchronous histogram's.
func (h *memHistogram) observe(point coremetrics.HistogramValue, delta bool) {
	validateObserved(point)
	state := h.observed
	restart := h.relayout(point.Bounds)
	//: a count that went down is a source that restarted.
	restart = restart || wentDown(point.Counts, state.previous)
	//: the reading itself unless a delta meter differences it.
	differenced := delta && !restart
	h.count.Store(h.storeWindow(point.Counts, differenced))
	sum := point.Sum
	//: the sum follows the counts' rule.
	if differenced {
		sum -= state.previousSum
	}
	state.previousSum = point.Sum
	h.sumBits.Store(math.Float64bits(sum))
	state.noSum = point.NoSum
	state.reported = true
}

// relayout rebuilds the series on bounds when they are not its layout, and
// reports whether it did — which a delta meter reads as a restart.
func (h *memHistogram) relayout(bounds []float64) bool {
	//: the layout the series already has.
	if slices.Equal(h.bounds, bounds) && h.counts != nil {
		return false
	}
	h.bounds = slices.Clone(bounds)
	h.counts, h.observed.previous = newLayout(len(bounds) + 1)
	h.observed.previousSum = 0
	//: a new layout.
	return true
}

// newLayout allocates the atomic window and the previous reading of a
// layout of buckets buckets.
func newLayout(buckets int) (window []atomic.Uint64, previous []uint64) {
	//: both zeroed, both of the layout's length.
	return make([]atomic.Uint64, buckets), make([]uint64, buckets)
}

// wentDown reports whether any count fell below its previous reading.
func wentDown(counts, previous []uint64) bool {
	//: bucket by bucket.
	for i, c := range counts {
		//: a source that restarted.
		if c < previous[i] {
			return true
		}
	}
	//: every count held or grew.
	return false
}

// storeWindow writes each bucket's window — the reading, or its growth since
// the previous one when differenced — keeps the reading as the previous, and
// returns the window's total.
func (h *memHistogram) storeWindow(counts []uint64, differenced bool) uint64 {
	previous := h.observed.previous
	var total uint64
	//: the window of each bucket.
	for i, c := range counts {
		window := c
		//: a delta meter reports the growth since the last reading.
		if differenced {
			window -= previous[i]
		}
		h.counts[i].Store(window)
		previous[i] = c
		total += window
	}
	//: the count the window holds.
	return total
}

// validateObserved panics with InvalidBuckets on a reading the meter would
// refuse at a synchronous Histogram's creation: NaN, an infinity, a bound
// not above the one before it, or counts that are not one per bucket plus
// the +Inf overflow.
func validateObserved(point coremetrics.HistogramValue) {
	//: one count per bucket, the overflow included.
	if len(point.Counts) != len(point.Bounds)+1 {
		panic(coremetrics.InvalidBuckets.Error())
	}
	//: a strictly increasing, finite ladder — a source's own, never sorted here.
	for i, b := range point.Bounds {
		//: NaN, an infinity, or a bound out of order.
		if math.IsNaN(b) || math.IsInf(b, 0) || (i > 0 && b <= point.Bounds[i-1]) {
			panic(coremetrics.InvalidBuckets.Error())
		}
	}
}

// observedSnapshot is snapshot for an observed series: the window observe
// wrote, read, never swapped — the callback writes it afresh each
// collection. A delta collection of a series the callback did not report is
// an empty window.
func (h *memHistogram) observedSnapshot(delta bool) coremetrics.HistogramValue {
	state := h.observed
	point := coremetrics.HistogramValue{
		Bounds: slices.Clone(h.bounds),
		Counts: make([]uint64, len(h.counts)),
		NoSum:  state.noSum,
	}
	//: unreported under delta: no observation in this window.
	if delta && !state.reported {
		return point
	}
	state.reported = false
	//: the window observe wrote.
	for i := range h.counts {
		point.Counts[i] = h.counts[i].Load()
	}
	point.Count = h.count.Load()
	//: a source with no total reports none.
	if !state.noSum {
		point.Sum = math.Float64frombits(h.sumBits.Load())
	}
	//: the reading.
	return point
}
