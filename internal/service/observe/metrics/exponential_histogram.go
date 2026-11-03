package metrics

import (
	"context"
	"math"
	"slices"
	"sync"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// The exponential histogram's constants.
const (
	// expExemplarSlots is how many exemplars an exponential series keeps: one,
	// since its buckets move whenever the scale drops.
	expExemplarSlots int = 1
	// minNormal is the smallest normal double, 2^-1022: a subnormal
	// magnitude is counted as it.
	minNormal float64 = 0x1p-1022
)

// expRange is one range — positive or negative — of an exponential
// histogram: counts[i] counts the bucket at index offset+i, in a backing
// array of the histogram's maxSize allocated at creation, so recording never
// allocates.
type expRange struct {
	// counts holds the buckets in use, counts[:size] of the backing array.
	counts []uint64
	// offset is the index of counts[0]; size how many buckets are in use.
	offset int32
	size   int
}

// memExpHistogram is an exponential (base-2) histogram series (ADR 0044,
// resolved): the OpenTelemetry specification's mapping, the scale lowered
// when a range would need more than maxSize buckets. A mutex guards it — a
// scale change rewrites every bucket, which no set of atomics can do — and
// the critical section is an index computation and an increment.
type memExpHistogram struct {
	mu sync.Mutex
	// maxSize bounds each range's buckets.
	maxSize int
	// scale is the current resolution; it only drops, and a delta collection
	// resets it to ExponentialMaxScale with the buckets.
	scale int32
	// positive and negative hold the two ranges' buckets.
	positive, negative expRange
	// zeroCount, sum and count are the point's totals.
	zeroCount uint64
	sum       float64
	count     uint64
	// ex keeps the series' one exemplar.
	ex exemplarSlots
}

// newExpHistogram builds an exponential histogram of maxSize buckets per
// range at ExponentialMaxScale: DefaultExponentialMaxSize for a maxSize of
// zero or less, InvalidBuckets for one — a range a scale change cannot work
// with.
func newExpHistogram(maxSize int) *memExpHistogram {
	//: the documented default, never a range of no bucket.
	if maxSize <= 0 {
		maxSize = coremetrics.DefaultExponentialMaxSize
	}
	//: one bucket cannot hold two magnitudes at any scale.
	if maxSize == 1 {
		panic(coremetrics.InvalidBuckets.Error())
	}
	//: both ranges allocated once, here.
	return &memExpHistogram{
		maxSize:  maxSize,
		scale:    coremetrics.ExponentialMaxScale,
		positive: expRange{counts: make([]uint64, maxSize)},
		negative: expRange{counts: make([]uint64, maxSize)},
	}
}

// Record observes value: its bucket, the sum and the count. NaN and the
// infinities have no bucket and are dropped; zero is counted in the zero
// bucket.
func (h *memExpHistogram) Record(value float64) {
	//: the exemplar is the only reader of whether it counted.
	h.record(value)
}

// RecordDuration observes d as seconds (latency-histogram convenience).
func (h *memExpHistogram) RecordDuration(d time.Duration) {
	//: latency histograms record the duration in seconds.
	h.record(d.Seconds())
}

// RecordContext records value as Record does and, when ctx carries a sampled
// span, keeps it as the series' exemplar — the
// core/observe/metrics.ContextHistogram sibling.
func (h *memExpHistogram) RecordContext(ctx context.Context, value float64) {
	//: a dropped value keeps no exemplar.
	if !h.record(value) {
		return
	}
	//: a sampled span makes the measurement the series' exemplar.
	if span, ok := sampledSpan(ctx); ok {
		h.ex.offer(expExemplarSlots, 0, value, span)
	}
}

// setClock implements setClocker: the exemplar's time is the meter's.
func (h *memExpHistogram) setClock(clk clock.Clock) {
	h.ex.setClock(clk)
}

// record observes value and reports whether it counted.
func (h *memExpHistogram) record(value float64) bool {
	//: no bucket holds NaN or an infinity.
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.count++
	h.sum += value
	//: the zero bucket, threshold 0.
	if value == 0 {
		h.zeroCount++
		return true
	}
	target := &h.positive
	//: a negative value counts by magnitude in the negative range.
	if value < 0 {
		target = &h.negative
	}
	//: a subnormal counts as the smallest normal, as the specification allows:
	//: the floor scale then holds every double in two buckets.
	index := coremetrics.ExponentialIndex(max(math.Abs(value), minNormal), h.scale)
	//: the range cannot hold index at this scale: lower it until it can.
	if change := h.downscaleFor(target, index); change > 0 {
		h.downscale(change)
		index >>= change
	}
	target.add(index)
	return true
}

// downscaleFor is how far the scale must drop for r to hold index within
// maxSize buckets: 0 when it already can.
func (h *memExpHistogram) downscaleFor(r *expRange, index int32) int32 {
	//: an empty range holds any index.
	if r.size == 0 {
		return 0
	}
	low, high := min(r.offset, index), max(r.offset+int32(r.size)-1, index)
	var change int32
	//: each step halves the index span; the floor scale holds any span of doubles.
	for int(high>>change-low>>change) >= h.maxSize && h.scale-change > coremetrics.ExponentialMinScale {
		change++
	}
	//: the drop, possibly none.
	return change
}

// downscale lowers the scale by change, merging each range's buckets 2^change
// into one.
func (h *memExpHistogram) downscale(change int32) {
	h.scale -= change
	h.positive.merge(change)
	h.negative.merge(change)
}

// add counts one value in the bucket at index, sliding or growing the range
// within its backing array; downscaleFor has made room.
func (r *expRange) add(index int32) {
	//: the first bucket of the range.
	if r.size == 0 {
		r.offset, r.size = index, 1
		r.counts[0] = 1
		return
	}
	//: below the range: slide the buckets up to open slots at the bottom.
	if index < r.offset {
		shift := int(r.offset - index)
		copy(r.counts[shift:r.size+shift], r.counts[:r.size])
		clear(r.counts[:shift])
		r.offset = index
		r.size += shift
	}
	slot := int(index - r.offset)
	//: above the range: grow it; the new slots are already zero.
	if slot >= r.size {
		clear(r.counts[r.size : slot+1])
		r.size = slot + 1
	}
	r.counts[slot]++
}

// merge folds the range's buckets 2^change into one: the bucket at index i
// becomes the bucket at i>>change of the lower scale. A merged slot never
// lies above the slot it is read from, so one ascending pass is enough.
func (r *expRange) merge(change int32) {
	//: an empty range has nothing to fold.
	if r.size == 0 {
		return
	}
	newOffset := r.offset >> change
	//: each old bucket into the one that now covers it.
	for i := range r.size {
		count := r.counts[i]
		r.counts[i] = 0
		r.counts[int((r.offset+int32(i))>>change-newOffset)] += count
	}
	r.size = int((r.offset+int32(r.size)-1)>>change-newOffset) + 1
	r.offset = newOffset
}

// snapshot copies the series into the exportable point type, consuming the
// window — and restoring the top scale — when the meter is a delta reader.
func (h *memExpHistogram) snapshot(delta bool) coremetrics.ExponentialHistogramValue {
	exemplars := h.ex.collect(delta)
	h.mu.Lock()
	defer h.mu.Unlock()
	point := coremetrics.ExponentialHistogramValue{
		Scale:     h.scale,
		ZeroCount: h.zeroCount,
		Positive:  h.positive.value(),
		Negative:  h.negative.value(),
		Sum:       h.sum,
		Count:     h.count,
		Exemplars: exemplars,
	}
	//: a delta window hands everything over and starts again at full resolution.
	if delta {
		h.positive.reset()
		h.negative.reset()
		h.zeroCount, h.sum, h.count = 0, 0, 0
		h.scale = coremetrics.ExponentialMaxScale
	}
	//: the point.
	return point
}

// value copies the range into its exportable form, nil counts when empty.
func (r *expRange) value() coremetrics.ExponentialBucketsValue {
	//: an empty range is the zero value.
	if r.size == 0 {
		return coremetrics.ExponentialBucketsValue{}
	}
	//: the buckets in use, copied.
	return coremetrics.ExponentialBucketsValue{Offset: r.offset, Counts: slices.Clone(r.counts[:r.size])}
}

// reset empties the range.
func (r *expRange) reset() {
	clear(r.counts[:r.size])
	r.offset, r.size = 0, 0
}
