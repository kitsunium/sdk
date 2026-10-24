// Package loop — the platform-neutral parts of the engine: sizing, the slab,
// the timer wheel, the post queue and the connection core over a fake driver.
package loop

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// heapMapper maps arenas on the Go heap, for the tests that need no kernel.
func heapMapper(n int) ([]byte, error) {
	return make([]byte, n), nil
}

// TestResolve pins the one ring formula of ADR 0186 §D5 and its refusals.
func TestResolve(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		v         corenet.LoopValue
		maxConns  int
		rlimit    uint64
		rings     int
		source    string
		table     int
		wantError errs.Code
	}
	tests := []tc{
		{name: "zero limit refused before any division", rlimit: 0, wantError: corenet.CodeLoopNoDescriptors},
		{name: "zero limit refused whatever Rings says", v: corenet.LoopValue{Rings: 4}, rlimit: 0, wantError: corenet.CodeLoopNoDescriptors},
		{name: "no bound: one ring", rlimit: 1024, rings: 1, source: ringsSingle, table: 1024},
		{name: "explicit wins", v: corenet.LoopValue{Rings: 3}, maxConns: 1 << 30, rlimit: 1024, rings: 3, source: ringsExplicit, table: 1024},
		{name: "MaxConns over the limit", maxConns: 300, rlimit: 128, rings: 3, source: ringsDerived, table: 128},
		{name: "SoftConns over MaxConns", v: corenet.LoopValue{SoftConns: 1000}, maxConns: 10, rlimit: 256, rings: 4, source: ringsDerived, table: 256},
		{name: "exactly one table", maxConns: 128, rlimit: 128, rings: 1, source: ringsSingle, table: 128},
		{name: "the fixed-file ceiling caps a table", maxConns: 3 << 20, rlimit: 1 << 40, rings: 3, source: ringsDerived, table: 1 << 20},
		{name: "negative bounds count as none", v: corenet.LoopValue{SoftConns: -5}, maxConns: -1, rlimit: 64, rings: 1, source: ringsSingle, table: 64},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			sz, err := resolve(c.v, c.maxConns, c.rlimit)
			if c.wantError != 0 {
				if !errs.HasCode(err, c.wantError) {
					t.Fatalf("resolve = %v, want %v", err, c.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if sz.rings != c.rings || sz.ringsSource != c.source || sz.tableSize != c.table {
				t.Fatalf("rings=%d (%s) table=%d, want %d (%s) %d", sz.rings, sz.ringsSource, sz.tableSize, c.rings, c.source, c.table)
			}
			if sz.loops != 1 || sz.cqBatch != defaultCQBatch || sz.spillCap != defaultSpillCap || sz.outputCap != defaultOutputCap {
				t.Fatalf("defaults not applied: %+v", sz)
			}
		})
	}
}

// TestSlab pins the size classes, the budget, and that a chunk's bytes never
// reach its neighbour's.
func TestSlab(t *testing.T) {
	t.Parallel()
	s := newSlab(64<<10, 256<<10, heapMapper)
	if got := classOf(1); got != 0 {
		t.Fatalf("classOf(1) = %d", got)
	}
	if got := classOf(2049); got != 1 {
		t.Fatalf("classOf(2049) = %d", got)
	}
	if _, ok := s.alloc(64<<10 + 1); ok {
		t.Fatal("a chunk past the largest class was handed out")
	}
	var held []chunkRef
	for {
		ch, ok := s.alloc(16 << 10)
		if !ok {
			break
		}
		b := s.bytes(ch)
		if len(b) != 16<<10 || cap(b) != 16<<10 {
			t.Fatalf("chunk len %d cap %d", len(b), cap(b))
		}
		for i := range b {
			b[i] = byte(len(held))
		}
		held = append(held, ch)
	}
	if len(held) != 16 {
		t.Fatalf("budget of 256 KiB held %d chunks of 16 KiB, want 16", len(held))
	}
	for i, ch := range held {
		if !bytes.Equal(s.bytes(ch), bytes.Repeat([]byte{byte(i)}, 16<<10)) {
			t.Fatalf("chunk %d was overwritten by a neighbour", i)
		}
	}
	for _, ch := range held {
		s.free(ch)
	}
	if s.inUse != 0 {
		t.Fatalf("inUse = %d after every free", s.inUse)
	}
	s.free(chunkRef{idx: noChunk})
}

// TestWheel pins that a timer fires no earlier than its tick and at most one
// advance late, over several turns, and that disarm and re-arm hold.
func TestWheel(t *testing.T) {
	t.Parallel()
	const n = 64
	w := newWheel(0, 0)
	w.grow(n)
	rng := rand.New(rand.NewPCG(1, 2))
	want := make([]int64, n)
	for s := range int32(n) {
		want[s] = 1 + rng.Int64N(5*wheelSlots)
		w.arm(s, want[s])
	}
	w.disarm(7)
	want[7] = 0
	fired := make([]int64, n)
	for now := int64(1); now <= 6*wheelSlots; now += 1 + rng.Int64N(3) {
		w.advance(now, func(s int32) {
			if fired[s] != 0 {
				t.Errorf("slot %d fired twice", s)
			}
			fired[s] = now
			if now < want[s] {
				t.Errorf("slot %d fired at %d, armed for %d", s, now, want[s])
			}
			if now > want[s]+3 {
				t.Errorf("slot %d fired at %d, armed for %d: late", s, now, want[s])
			}
		})
	}
	for s := range n {
		if (want[s] != 0) != (fired[s] != 0) {
			t.Errorf("slot %d armed for %d fired at %d", s, want[s], fired[s])
		}
	}
	// A callback that disarms and re-arms other slots never corrupts a list.
	w2 := newWheel(4, 0)
	for s := range int32(4) {
		w2.arm(s, 5)
	}
	w2.advance(5, func(s int32) {
		w2.disarm((s + 1) % 4)
		w2.arm((s+2)%4, 9)
	})
	got := 0
	w2.advance(20, func(int32) { got++ })
	if got == 0 || got > 4 {
		t.Fatalf("re-armed slots fired %d times", got)
	}
}

// TestPostQueue_exactlyOnce is the stress the protocol owes: N goroutines
// post while the consumer drains, and every post runs exactly once.
func TestPostQueue_exactlyOnce(t *testing.T) {
	t.Parallel()
	wakes := make(chan struct{}, 1)
	q := newPostQueue(func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	})
	const posters, each = 8, 5000
	var seen [posters * each]atomic.Int32
	run := func(_ corenet.LoopIO, arg uintptr) { seen[arg].Add(1) }
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-wakes:
				q.drain(nil)
			case <-stop:
				q.drain(nil)
				return
			}
		}
	}()
	var wg sync.WaitGroup
	for p := range posters {
		wg.Go(func() {
			for i := range each {
				for q.post(run, uintptr(p*each+i)) != nil {
					time.Sleep(time.Microsecond)
				}
			}
		})
	}
	wg.Wait()
	close(stop)
	<-done
	for i := range seen {
		if n := seen[i].Load(); n != 1 {
			t.Fatalf("post %d ran %d times", i, n)
		}
	}
	q.close()
	if err := q.post(run, 0); !errors.Is(err, corenet.ServerClosed) {
		t.Fatalf("post after close = %v", err)
	}
}

// TestPostQueue_full pins the refusal of a full queue.
func TestPostQueue_full(t *testing.T) {
	t.Parallel()
	q := newPostQueue(func() {})
	nop := func(corenet.LoopIO, uintptr) {}
	for range postQueueCap {
		if err := q.post(nop, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.post(nop, 0); !errors.Is(err, corenet.LoopPostQueueFull) {
		t.Fatalf("post on a full queue = %v", err)
	}
	if n := q.drain(nil); n != postQueueCap {
		t.Fatalf("drained %d", n)
	}
}
