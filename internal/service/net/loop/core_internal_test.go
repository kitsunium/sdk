// Package loop — the connection core, driven by a fake backend, on every
// platform.
package loop

import (
	"bytes"
	"errors"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// fakeDriver records what the core asks of a backend: each slot's sent bytes,
// at most limit per flush, and its shutdowns.
type fakeDriver struct {
	sent   map[int32]*bytes.Buffer
	limit  int
	closed []int32
}

func (d *fakeDriver) flush(r *ring, slot int32) {
	c := &r.conns[slot]
	data := r.lp.slab.data(c.out)
	if d.limit > 0 && len(data) > d.limit {
		data = data[:d.limit]
	}
	if d.sent[slot] == nil {
		d.sent[slot] = &bytes.Buffer{}
	}
	d.sent[slot].Write(data)
	c.out.off += int32(len(data))
	if c.out.off == c.out.len {
		r.lp.slab.free(c.out)
		c.out = chunkRef{idx: noChunk}
	}
	r.sent(slot, len(data))
}

func (d *fakeDriver) shutdown(r *ring, slot int32) {
	d.closed = append(d.closed, slot)
	r.release(slot)
}

func (d *fakeDriver) unread(*ring, int32) bool { return false }

// scriptH is a handler whose OnData consumes what consume says and records
// every buffer it saw and every close.
type scriptH struct {
	consume func(buf []byte) int
	onData  func(io corenet.LoopIO, ref corenet.LoopConnRef, buf []byte)
	seen    [][]byte
	closes  []error
	opens   []corenet.LoopConnRef
	timeout []corenet.LoopConnRef
	batches int
}

func (h *scriptH) OnOpen(_ corenet.LoopIO, ref corenet.LoopConnRef) { h.opens = append(h.opens, ref) }

func (h *scriptH) OnData(io corenet.LoopIO, ref corenet.LoopConnRef, buf []byte) int {
	h.seen = append(h.seen, append([]byte(nil), buf...))
	if h.onData != nil {
		h.onData(io, ref, buf)
	}
	if h.consume == nil {
		return len(buf)
	}
	return h.consume(buf)
}

func (h *scriptH) OnTimeout(_ corenet.LoopIO, ref corenet.LoopConnRef) {
	h.timeout = append(h.timeout, ref)
}
func (h *scriptH) OnWritable(corenet.LoopIO, corenet.LoopConnRef) {}
func (h *scriptH) OnBatchEnd(corenet.LoopIO)                      { h.batches++ }
func (h *scriptH) OnClose(_ corenet.LoopIO, _ corenet.LoopConnRef, err error) {
	h.closes = append(h.closes, err)
}

// newTestLoop builds a one-ring loop over a fake driver.
func newTestLoop(h corenet.LoopHandler, v corenet.LoopValue, idle time.Duration) (*loop, *ring, *fakeDriver) {
	sz, err := resolve(v, 0, 64)
	if err != nil {
		panic(err)
	}
	g := &Group{sz: sz, grp: &shared{}}
	lp := newLoop(g, Config{Handler: h, IdleTimeout: idle, Value: v}, 0, heapMapper)
	lp.post = newPostQueue(func() {})
	d := &fakeDriver{sent: map[int32]*bytes.Buffer{}}
	r := newRing(lp, 0, d)
	lp.rings = []*ring{r}
	return lp, r, d
}

// accept opens one connection on r.
func accept(t *testing.T, r *ring) int32 {
	t.Helper()
	slot, ok := r.take(int32(100))
	if !ok {
		t.Fatal("table full")
	}
	r.opened(slot)
	return slot
}

// TestCore_partialConsume pins the spill: an unconsumed tail comes back first,
// joined to the next bytes, and a full spill closes with LoopSpillFull.
func TestCore_partialConsume(t *testing.T) {
	t.Parallel()
	h := &scriptH{consume: func(buf []byte) int {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			return 0
		}
		return i + 1
	}}
	lp, r, _ := newTestLoop(h, corenet.LoopValue{SpillCap: 4096}, 0)
	s := accept(t, r)
	r.deliver(s, []byte("GET /a\nGET"))
	r.deliver(s, []byte(" /b\nGE"))
	h.OnBatchEnd(lp)
	lp.endBatch(time.Now())
	r.deliver(s, []byte("T /c\n"))
	want := []string{"GET /a\nGET", "GET /b\nGE", "GET /c\n"}
	if len(h.seen) != len(want) {
		t.Fatalf("seen %q", h.seen)
	}
	for i, w := range want {
		if string(h.seen[i]) != w {
			t.Fatalf("OnData %d = %q, want %q", i, h.seen[i], w)
		}
	}
	if r.hasSpill(s) {
		t.Fatal("a consumed spill is still held")
	}
	r.deliver(s, bytes.Repeat([]byte{'x'}, 5000))
	if len(h.closes) != 1 || !errors.Is(h.closes[0], corenet.LoopSpillFull) {
		t.Fatalf("closes = %v, want LoopSpillFull", h.closes)
	}
}

// TestCore_spillHandedOutStaysPut pins that a spill buffer handed to OnData
// is neither moved nor overwritten before OnBatchEnd, even when more bytes
// arrive in the same batch and need a larger chunk.
func TestCore_spillHandedOutStaysPut(t *testing.T) {
	t.Parallel()
	var held [][]byte
	var snap [][]byte
	h := &scriptH{consume: func([]byte) int { return 0 }}
	h.onData = func(_ corenet.LoopIO, _ corenet.LoopConnRef, buf []byte) {
		held = append(held, buf)
		snap = append(snap, append([]byte(nil), buf...))
	}
	lp, r, _ := newTestLoop(h, corenet.LoopValue{SpillCap: 60000}, 0)
	s := accept(t, r)
	for i := range 6 {
		r.deliver(s, bytes.Repeat([]byte{byte('a' + i)}, 3000))
	}
	for i := range held {
		if !bytes.Equal(held[i], snap[i]) {
			t.Fatalf("buffer %d changed before OnBatchEnd", i)
		}
	}
	lp.endBatch(time.Now())
	if got := lp.slab.data(r.conns[s].spill); len(got) != 18000 {
		t.Fatalf("spill holds %d bytes, want 18000", len(got))
	}
}

// TestCore_writeOrderAndStale pins that writes leave in order, that a closed
// connection's reference is stale, and that a slot reused gets a new
// generation the old reference cannot reach.
func TestCore_writeOrderAndStale(t *testing.T) {
	t.Parallel()
	h := &scriptH{}
	lp, r, d := newTestLoop(h, corenet.LoopValue{}, 0)
	d.limit = 3
	s := accept(t, r)
	ref := h.opens[0]
	for _, p := range []string{"one ", "two ", "three"} {
		if err := lp.Write(ref, []byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	for range 10 {
		r.markDirty(s)
		lp.endBatch(time.Now())
	}
	if got := d.sent[s].String(); got != "one two three" {
		t.Fatalf("sent %q", got)
	}
	if err := lp.Close(ref); err != nil {
		t.Fatal(err)
	}
	if err := lp.Close(ref); !errors.Is(err, corenet.LoopStaleRef) {
		t.Fatalf("second Close = %v", err)
	}
	lp.endBatch(time.Now())
	if len(h.closes) != 1 || h.closes[0] != nil {
		t.Fatalf("closes = %v", h.closes)
	}
	if err := lp.Write(ref, []byte("late")); !errors.Is(err, corenet.LoopStaleRef) {
		t.Fatalf("Write on a closed ref = %v", err)
	}
	s2 := accept(t, r)
	if s2 != s || h.opens[1].Gen == ref.Gen {
		t.Fatalf("slot %d gen %d reused as %d gen %d", s, ref.Gen, s2, h.opens[1].Gen)
	}
	if err := lp.Write(ref, []byte("stale")); !errors.Is(err, corenet.LoopStaleRef) {
		t.Fatalf("Write on the old generation = %v", err)
	}
	if lp.Pressure(ref) {
		t.Fatal("a stale ref reports pressure")
	}
	if err := lp.Write(corenet.LoopConnRef{}, []byte("x")); !errors.Is(err, corenet.LoopStaleRef) {
		t.Fatalf("Write on the zero ref = %v", err)
	}
}

// TestCore_outputCap pins LoopOutputFull and that nothing is copied.
func TestCore_outputCap(t *testing.T) {
	t.Parallel()
	h := &scriptH{}
	lp, r, _ := newTestLoop(h, corenet.LoopValue{OutputCap: 4096}, 0)
	s := accept(t, r)
	ref := h.opens[0]
	if err := lp.Write(ref, make([]byte, 4000)); err != nil {
		t.Fatal(err)
	}
	if err := lp.Write(ref, make([]byte, 100)); !errors.Is(err, corenet.LoopOutputFull) {
		t.Fatalf("Write past the cap = %v", err)
	}
	if got := r.conns[s].pending(); got != 4000 {
		t.Fatalf("pending %d after a refused Write", got)
	}
}

// TestCore_deadlineAndIdle pins OnTimeout, its rounding up, and the idle close.
func TestCore_deadlineAndIdle(t *testing.T) {
	t.Parallel()
	h := &scriptH{}
	lp, r, _ := newTestLoop(h, corenet.LoopValue{}, 100*time.Millisecond)
	accept(t, r)
	ref := h.opens[0]
	lp.epoch = time.Now()
	if err := lp.SetDeadline(ref, 25*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	at := r.wheel.armed(int32(ref.Slot))
	for tick := lp.tick; tick < at; tick++ {
		lp.tick = tick
		r.wheel.advance(tick, r.expire)
		if len(h.timeout) != 0 {
			t.Fatalf("OnTimeout at tick %d, armed for %d", tick, at)
		}
	}
	lp.tick = at
	r.wheel.advance(at, r.expire)
	if len(h.timeout) != 1 {
		t.Fatalf("OnTimeout fired %d times at its tick", len(h.timeout))
	}
	lp.tick = at + 20
	r.wheel.advance(lp.tick, r.expire)
	if len(h.closes) != 1 || !errors.Is(h.closes[0], errIdleSentinel()) {
		t.Fatalf("closes = %v, want the idle close", h.closes)
	}
}

// errIdleSentinel is the error an idle close carries.
func errIdleSentinel() error { return closeErr(rIdle, 0) }

// TestCore_pressure pins the soft flag set at accept past SoftConns.
func TestCore_pressure(t *testing.T) {
	t.Parallel()
	h := &scriptH{}
	lp, r, _ := newTestLoop(h, corenet.LoopValue{SoftConns: 2}, 0)
	for range 3 {
		accept(t, r)
	}
	got := []bool{lp.Pressure(h.opens[0]), lp.Pressure(h.opens[1]), lp.Pressure(h.opens[2])}
	if got[0] || got[1] || !got[2] {
		t.Fatalf("pressure = %v, want only the third", got)
	}
}

// TestCore_evict pins that eviction takes the least recent idle connection,
// never one holding a spill.
func TestCore_evict(t *testing.T) {
	t.Parallel()
	h := &scriptH{consume: func([]byte) int { return 0 }}
	lp, r, _ := newTestLoop(h, corenet.LoopValue{EvictIdle: corenet.DurationValue(50 * time.Millisecond)}, 0)
	a := accept(t, r)
	b := accept(t, r)
	r.deliver(a, []byte("partial"))
	lp.tick += 100
	if n := r.evict(); n != 1 {
		t.Fatalf("evicted %d, want 1", n)
	}
	if r.conns[b].state != stFree || r.conns[a].state != stOpen {
		t.Fatalf("a=%d b=%d: the one mid-request was evicted", r.conns[a].state, r.conns[b].state)
	}
}

// TestCore_allocs pins the LoopIO budget: allocs 0 on Write, SetDeadline,
// Pressure and Close with a caller-owned p.
func TestCore_allocs(t *testing.T) {
	h := &scriptH{}
	lp, r, _ := newTestLoop(h, corenet.LoopValue{}, time.Second)
	s := accept(t, r)
	ref := h.opens[0]
	p := []byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
	var io corenet.LoopIO = lp
	n := testing.AllocsPerRun(1000, func() {
		_ = io.Write(ref, p)
		_ = io.SetDeadline(ref, time.Second)
		_ = io.Pressure(ref)
		r.markDirty(s)
		lp.endBatch(time.Now())
	})
	if n != 0 {
		t.Fatalf("LoopIO allocates %.1f per call", n)
	}
	if n := testing.AllocsPerRun(100, func() { _ = io.Write(corenet.LoopConnRef{}, p) }); n != 0 {
		t.Fatalf("a refused Write allocates %.1f", n)
	}
}

// TestCore_yieldBound pins the yield's bound (ADR 0199): a loop yields at the
// first batch end, then once sz.yield has passed since its last yield and
// never sooner, whatever the batches in between; with no spare P it never
// does.
func TestCore_yieldBound(t *testing.T) {
	t.Parallel()
	lp, _, _ := newTestLoop(&scriptH{}, corenet.LoopValue{}, 0)
	lp.sz.yield = yieldEvery
	t0 := time.Now()
	for _, c := range []struct {
		at   time.Duration
		want bool
	}{
		{0, true},
		{time.Millisecond, false},
		{yieldEvery - time.Nanosecond, false},
		{yieldEvery, true},
		{yieldEvery + yieldEvery/2, false},
		{2*yieldEvery - time.Nanosecond, false},
		{3 * yieldEvery, true},
	} {
		lp.ended = t0.Add(c.at)
		if got := lp.yield(); got != c.want {
			t.Fatalf("yield at %v = %v, want %v", c.at, got, c.want)
		}
	}
	lp.sz.yield = 0
	lp.ended = t0.Add(time.Hour)
	if lp.yield() {
		t.Fatal("a loop with no spare P yielded")
	}
}

// TestCore_endBatchStampsEnd pins that the yield measures from the batch's
// end: endBatch records it, so a long batch counts toward the interval.
func TestCore_endBatchStampsEnd(t *testing.T) {
	t.Parallel()
	lp, _, _ := newTestLoop(&scriptH{}, corenet.LoopValue{}, 0)
	before := time.Now()
	lp.endBatch(before)
	if lp.ended.Before(before) {
		t.Fatalf("ended %v before the batch started %v", lp.ended, before)
	}
}
