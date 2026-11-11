package loop

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// BenchmarkCore_batch measures one batch with one active connection among
// open ones.
func BenchmarkCore_batch(b *testing.B) {
	for _, open := range []int{1, 10_000} {
		b.Run(benchName(open), func(b *testing.B) {
			lp, r := newOpenTestLoop(b, &answerH{}, open)
			slot := r.lruHead
			primeSpill(b, lp, r, slot)
			b.ReportAllocs()
			for b.Loop() {
				activeBatch(lp, r, slot)
			}
		})
	}
}

// BenchmarkCore_batchClock measures the clock of one batch: read at its start
// for the tick, at its end for the histogram and the yield, nothing between.
func BenchmarkCore_batchClock(b *testing.B) {
	lp, _, _ := newTestLoop(&scriptH{}, corenet.LoopValue{}, 0)
	b.ReportAllocs()
	for b.Loop() {
		lp.begin()
		lp.endBatch()
	}
}
