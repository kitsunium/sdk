// Package loop — what one batch costs against the connections a ring holds
// open, on every platform.
package loop

import (
	"bytes"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// newOpenTestLoop builds a one-ring loop over a fake driver with room for n
// connections, and opens them.
func newOpenTestLoop(tb testing.TB, h corenet.LoopHandler, n int) (*loop, *ring) {
	tb.Helper()
	sz, err := resolve(corenet.LoopValue{}, 0, uint64(n+1))
	if err != nil {
		tb.Fatal(err)
	}
	g := &Group{sz: sz, grp: &shared{}}
	lp := newLoop(g, Config{Handler: h}, 0, heapMapper)
	lp.post = newPostQueue(func() {})
	r := newRing(lp, 0, &fakeDriver{sent: map[int32]*bytes.Buffer{}})
	lp.rings = []*ring{r}
	for range n {
		slot, ok := r.take(int32(100))
		if !ok {
			tb.Fatal("table full")
		}
		r.opened(slot)
	}
	return lp, r
}

// answerH consumes one line per OnData and answers it, as a keep-alive
// request-response handler does.
type answerH struct{ scriptH }

func (h *answerH) OnData(io corenet.LoopIO, ref corenet.LoopConnRef, buf []byte) int {
	i := bytes.IndexByte(buf, '\n')
	if i < 0 {
		return 0
	}
	_ = io.Write(ref, buf[:i+1])
	return i + 1
}

// activeBatch is one batch of a ring where slot alone is active: the end of
// a request held in its spill and the start of the next, so the spill's head
// is consumed and must be compacted at the batch's end.
func activeBatch(lp *loop, r *ring, slot int32) {
	r.deliver(slot, []byte("T /a\nGE"))
	lp.h.OnBatchEnd(lp)
	lp.endBatch()
}

// primeSpill leaves the start of a request in slot's spill.
func primeSpill(tb testing.TB, lp *loop, r *ring, slot int32) {
	tb.Helper()
	r.deliver(slot, []byte("GE"))
	lp.endBatch()
	if !r.hasSpill(slot) {
		tb.Fatal("no spill held")
	}
}

// spillIs fails unless slot's spill holds want from its chunk's front.
func spillIs(tb testing.TB, lp *loop, r *ring, slot int32, want string) {
	tb.Helper()
	c := &r.conns[slot]
	if c.spill.off != 0 || string(lp.slab.data(c.spill)) != want {
		tb.Fatalf("spill = %+v %q, want %q compacted", c.spill, lp.slab.data(c.spill), want)
	}
}

// batchCost is the fastest of rounds batches where one connection is active
// among open.
func batchCost(t *testing.T, open, rounds int) time.Duration {
	t.Helper()
	lp, r := newOpenTestLoop(t, &answerH{}, open)
	slot := r.lruHead
	primeSpill(t, lp, r, slot)
	best := time.Duration(1 << 62)
	for range rounds {
		started := time.Now()
		activeBatch(lp, r, slot)
		best = min(best, time.Since(started))
	}
	spillIs(t, lp, r, slot, "GE")
	return best
}

// TestCore_batchCostIndependentOfOpen pins that a batch costs what it
// touches: one active connection among ten thousand idle ones costs about
// what it costs alone. A walk of the open connections at every batch's end
// made it a hundred times dearer.
func TestCore_batchCostIndependentOfOpen(t *testing.T) {
	t.Parallel()
	const rounds = 400
	small := batchCost(t, 8, rounds)
	large := batchCost(t, 10_000, rounds)
	// The fastest of many rounds discards the scheduler's noise; the bound
	// leaves room for the cache misses of a larger table.
	if large > 8*small+20*time.Microsecond {
		t.Fatalf("a batch costs %v with 10000 open connections, %v with 8: it walks the open ones", large, small)
	}
}

// TestCore_compactTouchedOnly pins the compaction: a spill whose head was
// consumed starts again at its chunk's front once the batch ended, and a slot
// released and taken again inside the batch is no trouble.
func TestCore_compactTouchedOnly(t *testing.T) {
	t.Parallel()
	h := &answerH{}
	lp, r := newOpenTestLoop(t, h, 3)
	a, b := r.lruHead, r.conns[r.lruHead].lruNext
	r.deliver(a, []byte("x\ny"))
	r.deliver(a, []byte("\nz"))
	r.deliver(b, []byte("p\nq"))
	r.deliver(b, []byte("\nr"))
	if r.conns[a].spill.off == 0 || r.conns[b].spill.off == 0 {
		t.Fatal("the heads were not consumed")
	}
	r.fail(b, rNone, 0)
	again, ok := r.take(int32(101))
	if !ok || again != b {
		t.Fatalf("take = %d, %v; want the released slot %d", again, ok, b)
	}
	r.opened(again)
	lp.h.OnBatchEnd(lp)
	lp.endBatch()
	spillIs(t, lp, r, a, "z")
	if r.conns[a].flags&fCompact != 0 || len(r.compact) != 0 {
		t.Fatal("the compaction list survived the batch")
	}
	r.deliver(a, []byte("\n"))
	if r.hasSpill(a) {
		t.Fatal("the compacted spill lost its bytes")
	}
}

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

// benchName names a sub-benchmark by its open connections.
func benchName(open int) string {
	if open == 1 {
		return "open=1"
	}
	return "open=10000"
}
