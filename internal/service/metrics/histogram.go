// Package metrics — atomic bucketed histogram.
package metrics

import (
	"math"
	"slices"
	"sync/atomic"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
)

// memHistogram records observations into sorted upper-bound buckets plus a +Inf
// overflow slot; counts/sum/count are atomic for a lock-free Record.
type memHistogram struct {
	buckets []float64
	counts  []atomic.Uint64 // len == len(buckets)+1 (last = +Inf overflow)
	sumBits atomic.Uint64
	count   atomic.Uint64
}

// newHistogram builds a histogram with a sorted copy of buckets.
func newHistogram(buckets []float64) *memHistogram {
	//: defensively copy + sort so callers cannot mutate our boundaries.
	sorted := slices.Clone(buckets)
	slices.Sort(sorted)
	//: counts has one extra slot for the +Inf overflow bucket.
	return &memHistogram{buckets: sorted, counts: make([]atomic.Uint64, len(sorted)+1)}
}

// Record observes value into its bucket and updates the running sum/count.
func (h *memHistogram) Record(value float64) {
	//: the overflow slot catches values above every boundary.
	idx := len(h.buckets)
	//: the first bucket whose upper bound covers value wins.
	for i, bound := range h.buckets {
		//: value belongs to the first bucket it does not exceed.
		if value <= bound {
			//: found the covering bucket.
			idx = i
			break
		}
	}
	//: increment the chosen bucket + the total count.
	h.counts[idx].Add(1)
	h.count.Add(1)
	//: fold value into the running sum via CAS.
	for {
		//: read + decode the current sum.
		old := h.sumBits.Load()
		//: add this observation.
		next := math.Float64bits(math.Float64frombits(old) + value)
		//: publish iff uncontended.
		if h.sumBits.CompareAndSwap(old, next) {
			//: our update won.
			return
		}
	}
}

// snapshot copies the histogram into the exportable value type.
func (h *memHistogram) snapshot() coremetrics.HistogramValue {
	//: copy each atomic bucket count into a plain slice.
	counts := make([]uint64, len(h.counts))
	//: read every bucket lock-free.
	for i := range h.counts {
		//: copy the i-th bucket count.
		counts[i] = h.counts[i].Load()
	}
	//: assemble the point-in-time value.
	return coremetrics.HistogramValue{
		Buckets: slices.Clone(h.buckets),
		Counts:  counts,
		Sum:     math.Float64frombits(h.sumBits.Load()),
		Count:   h.count.Load(),
	}
}

// RecordDuration observes d as seconds (latency-histogram convenience).
func (h *memHistogram) RecordDuration(d time.Duration) {
	//: latency histograms record the duration in seconds.
	h.Record(d.Seconds())
}
