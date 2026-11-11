package loop

import (
	"math/bits"
	"sync/atomic"
	"time"
)

// batchBuckets is the batch-duration histogram's width: bucket i counts
// batches of under 2^i nanoseconds.
const batchBuckets int = 40

// observe records one batch's duration in the group's histogram.
func (g *shared) observe(d time.Duration) {
	i := bits.Len64(uint64(max(d, 0)))
	//: the last bucket holds everything longer.
	g.hist[min(i, batchBuckets-1)].Add(1)
}

// batchP99 returns the upper bound of the bucket holding the 99th
// percentile, zero before any batch.
func (g *shared) batchP99() time.Duration {
	var counts [batchBuckets]uint64
	var total uint64
	//: one snapshot of every bucket.
	for i := range counts {
		counts[i] = g.hist[i].Load()
		total += counts[i]
	}
	//: nothing measured.
	if total == 0 {
		//: zero.
		return 0
	}
	want := total - total/100
	var seen uint64
	//: the first bucket that reaches 99 % of the batches.
	for i, n := range counts {
		seen += n
		//: found.
		if seen >= want {
			//: the bucket's upper bound.
			return time.Duration(uint64(1) << uint(i))
		}
	}
	//: unreachable: the walk reaches total.
	return time.Duration(uint64(1) << uint(batchBuckets-1))
}

// storeU stores v to a, once it differs from what was published.
func storeU(a *atomic.Uint64, was *uint64, v uint64) {
	//: unchanged: nothing to publish.
	if v != *was {
		a.Store(v)
		*was = v
	}
}

// storeI is storeU for a gauge.
func storeI(a *atomic.Int64, was *int64, v int64) {
	//: unchanged: nothing to publish.
	if v != *was {
		a.Store(v)
		*was = v
	}
}

// publish copies the ring's counters to its atomics, those that moved.
func (r *ring) publish() {
	p, s, o := r.pub, &r.st, &r.seen
	storeI(&p.conns, &o.conns, int64(r.live))
	storeU(&p.accepted, &o.accepted, s.accepted)
	storeU(&p.closed, &o.closed, s.closed)
	storeU(&p.batches, &o.batches, s.batches)
	storeU(&p.completions, &o.completions, s.completions)
	storeU(&p.starved, &o.starved, s.starved)
	storeU(&p.retries, &o.retries, s.retries)
	storeU(&p.pauses, &o.pauses, s.pauses)
	storeU(&p.evicted, &o.evicted, s.evicted)
	storeU(&p.watchdog, &o.watchdog, s.watchdog)
	storeU(&p.cut, &o.cut, s.cut)
	storeU(&p.bytesIn, &o.bytesIn, s.bytesIn)
	storeU(&p.bytesOut, &o.bytesOut, s.bytesOut)
	storeU(&p.outRefused, &o.outRefused, s.outRefused)
	storeU(&p.spillClosed, &o.spillClosed, s.spillClosed)
	storeU(&p.timeouts, &o.timeouts, s.timeouts)
	storeU(&p.idleClosed, &o.idleClosed, s.idleClosed)
	storeI(&p.pending, &o.pending, s.pending)
	//: published when it flips.
	if r.draining != o.draining {
		p.draining.Store(r.draining)
		o.draining = r.draining
	}
}
