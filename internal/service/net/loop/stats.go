package loop

import (
	"math/bits"
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

// publish copies the ring's counters to its atomics.
func (r *ring) publish() {
	p, s := r.pub, &r.st
	p.conns.Store(int64(r.live))
	p.accepted.Store(s.accepted)
	p.closed.Store(s.closed)
	p.batches.Store(s.batches)
	p.completions.Store(s.completions)
	p.starved.Store(s.starved)
	p.retries.Store(s.retries)
	p.pauses.Store(s.pauses)
	p.evicted.Store(s.evicted)
	p.watchdog.Store(s.watchdog)
	p.draining.Store(r.draining)
}
