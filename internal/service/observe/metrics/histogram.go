package metrics

import (
	"context"
	"math"
	"slices"
	"sync/atomic"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// memHistogram records observations into sorted upper-bound buckets plus a +Inf
// overflow slot; counts/sum/count are atomic for a lock-free Record.
type memHistogram struct {
	bounds  []float64
	counts  []atomic.Uint64 // len == len(bounds)+1 (last = +Inf overflow)
	sumBits atomic.Uint64
	count   atomic.Uint64
	// ex keeps one exemplar per bucket: the last measurement RecordContext
	// was handed under a sampled span (ADR 0051).
	ex exemplarSlots
	// observed is the state of a series written by an ObservableHistogram
	// callback at collection time (#868), nil for a synchronous histogram.
	observed *observedState
}

// newHistogram builds a histogram with a sorted copy of bounds: DefaultBuckets
// for an empty layout, a loud InvalidBuckets refusal for NaN, an infinity or a
// repeated bound (#1135) — a structural mistake fixed at the call site, refused
// the way ValidateAttrs refuses an empty key.
func newHistogram(bounds []float64) *memHistogram {
	//: no layout: the documented default, never a single +Inf bucket.
	if len(bounds) == 0 {
		bounds = coremetrics.DefaultBuckets()
	}
	//: defensively copy + sort so callers cannot mutate our boundaries.
	sorted := slices.Clone(bounds)
	slices.Sort(sorted)
	validateBuckets(sorted)
	//: counts has one extra slot for the +Inf overflow bucket.
	return &memHistogram{bounds: sorted, counts: make([]atomic.Uint64, len(sorted)+1)}
}

// validateBuckets panics with InvalidBuckets when sorted holds NaN, an
// infinity or a repeated bound. slices.Sort puts NaN first, so the walk sees
// every bound once.
func validateBuckets(sorted []float64) {
	for i, b := range sorted {
		//: +Inf is the implicit overflow bucket; NaN and -Inf bound nothing.
		if math.IsNaN(b) || math.IsInf(b, 0) || (i > 0 && b == sorted[i-1]) {
			panic(coremetrics.InvalidBuckets.Error())
		}
	}
}

// Record observes value into its bucket and updates the running sum/count.
func (h *memHistogram) Record(value float64) {
	//: the bucket is only the exemplar's concern.
	h.record(value)
}

// RecordContext records value as Record does and, when ctx carries a sampled
// span, keeps it as the exemplar of the bucket it fell in — the
// core/observe/metrics.ContextHistogram sibling.
func (h *memHistogram) RecordContext(ctx context.Context, value float64) {
	idx := h.record(value)
	//: a sampled span makes the measurement its bucket's exemplar.
	if span, ok := sampledSpan(ctx); ok {
		h.ex.offer(len(h.counts), idx, value, span)
	}
}

// setClock implements setClocker: an exemplar's time is the meter's.
func (h *memHistogram) setClock(clk clock.Clock) {
	h.ex.setClock(clk)
}

// record observes value into its bucket, updates the running sum/count and
// returns the bucket's index.
func (h *memHistogram) record(value float64) int {
	//: the overflow slot catches values above every boundary.
	idx := len(h.bounds)
	//: the first bucket whose upper bound covers value wins.
	for i, bound := range h.bounds {
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
			return idx
		}
	}
}

// snapshot copies the histogram into the exportable point type, consuming the
// window when the meter is a delta reader.
//
// Under delta every slot is SWAPPED to zero rather than read, so an observation
// is reported exactly once. The swaps are separate atomics, so a Record racing
// a collection can land its bucket in one window and its sum in the next; the
// same skew already exists on the read path and the instruments are lock-free
// on purpose. What cannot happen is double counting, which is what a
// read-then-store would have allowed.
func (h *memHistogram) snapshot(delta bool) coremetrics.HistogramValue {
	//: an observed series holds the window its callback wrote.
	if h.observed != nil {
		return h.observedSnapshot(delta)
	}
	exemplars := h.ex.collect(delta)
	//: copy each atomic bucket count into a plain slice.
	counts := make([]uint64, len(h.counts))
	//: read — or consume — every bucket lock-free.
	for i := range h.counts {
		//: a delta window hands the count over; a cumulative one keeps it.
		if delta {
			//: read and reset in one step.
			counts[i] = h.counts[i].Swap(0)
			//: this bucket is done.
			continue
		}
		//: copy the i-th bucket count.
		counts[i] = h.counts[i].Load()
	}
	//: the two totals follow the same rule as the buckets.
	if delta {
		//: consume both windows.
		return coremetrics.HistogramValue{
			Bounds:    slices.Clone(h.bounds),
			Counts:    counts,
			Sum:       math.Float64frombits(h.sumBits.Swap(0)),
			Count:     h.count.Swap(0),
			Exemplars: exemplars,
		}
	}
	//: assemble the cumulative point-in-time value.
	return coremetrics.HistogramValue{
		Bounds:    slices.Clone(h.bounds),
		Counts:    counts,
		Sum:       math.Float64frombits(h.sumBits.Load()),
		Count:     h.count.Load(),
		Exemplars: exemplars,
	}
}

// RecordDuration observes d as seconds (latency-histogram convenience).
func (h *memHistogram) RecordDuration(d time.Duration) {
	//: latency histograms record the duration in seconds.
	h.Record(d.Seconds())
}
