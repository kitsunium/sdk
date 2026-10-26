// Package server — what one portable batch costs against the connections it
// holds open, and the timers it no longer scans at every batch.
package server

import (
	"bytes"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// portableLineH consumes one line per OnData and records the timeouts.
type portableLineH struct {
	timeouts []corenet.LoopConnRef
}

func (*portableLineH) OnOpen(corenet.LoopIO, corenet.LoopConnRef) {}

func (*portableLineH) OnData(_ corenet.LoopIO, _ corenet.LoopConnRef, buf []byte) int {
	i := bytes.IndexByte(buf, '\n')
	if i < 0 {
		return 0
	}
	return i + 1
}

func (h *portableLineH) OnTimeout(_ corenet.LoopIO, ref corenet.LoopConnRef) {
	h.timeouts = append(h.timeouts, ref)
}
func (*portableLineH) OnWritable(corenet.LoopIO, corenet.LoopConnRef)     {}
func (*portableLineH) OnBatchEnd(corenet.LoopIO)                          {}
func (*portableLineH) OnClose(corenet.LoopIO, corenet.LoopConnRef, error) {}

// newOpenPortable builds a portable loop holding open connections with no
// goroutine behind them, idle for less than its idle bound.
func newOpenPortable(h corenet.LoopHandler, open int) *portableLoop {
	pl := &portableLoop{
		h: h, spillCap: portableSpillCap, outCap: portableOutputCap, idle: time.Hour,
		events: make(chan portableEvent, portableEvents), wake: make(chan struct{}, 1),
		postOps: make([]corenet.PostFunc, 1), postArgs: make([]uintptr, 1),
	}
	now := time.Now()
	for range open {
		pl.conns = append(pl.conns, &portableConn{
			gen: 1, open: true, lastIO: now, ack: make(chan struct{}, 1), kick: make(chan struct{}, 1),
		})
	}
	pl.live.Store(int64(open))
	return pl
}

// portableActiveBatch is one batch where slot 0 alone is active: the end of
// a request held in its spill and the start of the next, so the spill's head
// is consumed and compacted at the batch's end.
func portableActiveBatch(pl *portableLoop) {
	pl.deliver(0, pl.conns[0], []byte("T /a\nGE"))
	pl.batch()
}

// portablePrime leaves the start of a request in slot 0's spill, and runs
// the first scan of the timers.
func portablePrime(tb testing.TB, pl *portableLoop) {
	tb.Helper()
	pl.deliver(0, pl.conns[0], []byte("GE"))
	pl.batch()
	if pc := pl.conns[0]; string(pc.spill[pc.spillOff:]) != "GE" {
		tb.Fatalf("spill = %q, want \"GE\"", pc.spill[pc.spillOff:])
	}
}

// portableBatchCost is the fastest of rounds batches where one connection is
// active among open.
func portableBatchCost(t *testing.T, open, rounds int) time.Duration {
	t.Helper()
	pl := newOpenPortable(&portableLineH{}, open)
	portablePrime(t, pl)
	best := time.Duration(1 << 62)
	for range rounds {
		started := time.Now()
		portableActiveBatch(pl)
		best = min(best, time.Since(started))
	}
	pc := pl.conns[0]
	if pc.spillOff != 0 || string(pc.spill) != "GE" || len(pl.compact) != 0 {
		t.Fatalf("spill = %q at %d, listed %v: not compacted", pc.spill, pc.spillOff, pl.compact)
	}
	return best
}

// TestPortable_batchCostIndependentOfOpen pins that a portable batch costs
// what it touches: one active connection among ten thousand idle ones costs
// about what it costs alone. The timers' scan and the compaction walked every
// connection at every batch, which made it a hundred times dearer.
func TestPortable_batchCostIndependentOfOpen(t *testing.T) {
	t.Parallel()
	const rounds = 400
	small := portableBatchCost(t, 8, rounds)
	large := portableBatchCost(t, 10_000, rounds)
	// The fastest of many rounds discards the scheduler's noise; a scan of
	// the timers that falls due inside the rounds is one round among them.
	if large > 8*small+20*time.Microsecond {
		t.Fatalf("a batch costs %v with 10000 open connections, %v with 8: it walks the open ones", large, small)
	}
}

// TestPortable_timersOnTick pins when the timers are scanned: once a tick,
// sooner when a deadline set since falls before it, and never late on a
// deadline.
func TestPortable_timersOnTick(t *testing.T) {
	t.Parallel()
	h := &portableLineH{}
	pl := newOpenPortable(h, 2)
	pl.batch()
	if pl.timersAt.IsZero() {
		t.Fatal("the first batch did not schedule the next scan")
	}
	// Pinned far out so the deadline below lowers it however long the batch took.
	pl.timersAt = time.Now().Add(time.Hour)
	ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
	if err := pl.SetDeadline(ref, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if want := pl.conns[1].deadline; !pl.timersAt.Equal(want) {
		t.Fatalf("next scan %v, want the deadline %v", pl.timersAt, want)
	}
	time.Sleep(2 * time.Millisecond)
	pl.batch()
	if len(h.timeouts) != 1 || h.timeouts[0] != ref {
		t.Fatalf("timeouts = %v, want %v", h.timeouts, ref)
	}
	// A deadline beyond the tick leaves the scan at the tick.
	if err := pl.SetDeadline(ref, time.Hour); err != nil {
		t.Fatal(err)
	}
	if !pl.timersAt.Before(pl.conns[1].deadline) {
		t.Fatal("a far deadline delayed the tick's scan")
	}
}

// BenchmarkPortable_batch measures one portable batch with one active
// connection among open ones.
func BenchmarkPortable_batch(b *testing.B) {
	for _, open := range []int{1, 10_000} {
		name := "open=1"
		if open > 1 {
			name = "open=10000"
		}
		b.Run(name, func(b *testing.B) {
			pl := newOpenPortable(&portableLineH{}, open)
			portablePrime(b, pl)
			b.ReportAllocs()
			for b.Loop() {
				portableActiveBatch(pl)
			}
		})
	}
}
