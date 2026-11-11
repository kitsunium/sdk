// Package loop — the benchmarks of the engine's own parts, on every platform:
// a Post from outside to its drain, the timer wheel, the slab and one Write.
// BENCH.md reports them beside the io_uring rows, with the command that
// regenerates it.
package loop

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// wakers are the wake-ups BenchmarkPost times a Post with, by name: none, the
// portable floor, and the platform's own, added by its file (an eventfd write
// on Linux).
var wakers = []struct {
	name string
	mk   func(tb testing.TB) func()
}{
	{"wake=none", func(testing.TB) func() { return func() {} }},
}

// BenchmarkPost measures one Post to its drain: the queue's mutex, the CAS that
// elects the poster that wakes the loop, the wake-up, and the pop that runs the
// operation. One goroutine plays both sides, so the cost of a wake-up that
// crosses threads — the kernel's — is not in it; allocs/op is 0.
func BenchmarkPost(b *testing.B) {
	for _, w := range wakers {
		b.Run(w.name, func(b *testing.B) {
			lp, _, _ := newTestLoop(&scriptH{}, corenet.LoopValue{}, 0)
			lp.post = newPostQueue(w.mk(b))
			op := func(corenet.LoopIO, uintptr) {}
			b.ReportAllocs()
			for b.Loop() {
				if err := lp.Post(op, 1); err != nil {
					b.Fatal(err)
				}
				lp.post.drain(lp)
			}
		})
	}
}

// wheelTimers is how many timers BenchmarkWheel arms.
const wheelTimers int = 10_000

// BenchmarkWheel measures the timer wheel: arming and disarming one slot, and
// one tick over ten thousand armed timers, of which about ten fire and re-arm.
func BenchmarkWheel(b *testing.B) {
	b.Run("arm+disarm", func(b *testing.B) {
		w := newWheel(wheelTimers, 0)
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			slot := int32(i % wheelTimers)
			w.arm(slot, int64(i%1000)+1)
			w.disarm(slot)
			i++
		}
	})
	b.Run("tick/timers=10000", func(b *testing.B) {
		w := newWheel(wheelTimers, 0)
		for s := range wheelTimers {
			w.arm(int32(s), int64(s%1000)+1)
		}
		fire := func(slot int32) { w.arm(slot, w.now+1000) }
		now := int64(0)
		b.ReportAllocs()
		for b.Loop() {
			now++
			w.advance(now, fire)
		}
	})
}

// BenchmarkSlab measures the slab: one alloc and its free in each size class a
// loop uses, once the arena is mapped, and a chunk grown in place
// (growOut: an alloc of the larger chunk, the copy, the free of the smaller).
func BenchmarkSlab(b *testing.B) {
	for _, size := range []int{2 << 10, 16 << 10, 64 << 10, 1 << 20} {
		b.Run("alloc+free/"+classLabel(size), func(b *testing.B) {
			s := newSlab(1<<20, 1<<30, heapMapper)
			warm, ok := s.alloc(size)
			if !ok {
				b.Fatal("alloc refused")
			}
			s.free(warm)
			b.ReportAllocs()
			for b.Loop() {
				ch, ok := s.alloc(size)
				if !ok {
					b.Fatal("alloc refused")
				}
				s.free(ch)
			}
		})
	}
	b.Run("growOut/2KiB-to-8KiB", func(b *testing.B) {
		lp, r := newOpenTestLoop(b, &answerH{}, 1)
		c := &r.conns[r.lruHead]
		warm, _ := lp.slab.alloc(8 << 10)
		lp.slab.free(warm)
		b.ReportAllocs()
		for b.Loop() {
			c.out, _ = lp.slab.alloc(1 << 10)
			c.out.len = 1 << 10
			if !lp.growOut(c, 6<<10) {
				b.Fatal("growOut refused")
			}
			lp.slab.free(c.out)
			c.out = chunkRef{idx: noChunk}
		}
	})
}

// classLabel names a chunk size for a sub-benchmark.
func classLabel(size int) string {
	switch {
	case size >= 1<<20:
		return "1MiB"
	case size >= 1<<10:
		return itoa(size>>10) + "KiB"
	}
	return itoa(size) + "B"
}

// itoa writes a small non-negative integer.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d [20]byte
	i := len(d)
	for ; n > 0; n /= 10 {
		i--
		d[i] = byte('0' + n%10)
	}
	return string(d[i:])
}

// BenchmarkWrite measures LoopIO.Write of one answer into a connection with no
// output queued: the slab chunk attached, the bytes copied, the slot listed for
// the batch's flush. The chunk is returned after each write, as the batch's
// flush would, so every write attaches a fresh one; allocs/op is 0.
func BenchmarkWrite(b *testing.B) {
	for _, size := range []int{240, 8 << 10} {
		b.Run("bytes="+itoa(size), func(b *testing.B) {
			lp, r := newOpenTestLoop(b, &answerH{}, 1)
			slot := r.lruHead
			ref := r.ref(slot)
			c := &r.conns[slot]
			p := make([]byte, size)
			b.ReportAllocs()
			for b.Loop() {
				if err := lp.Write(ref, p); err != nil {
					b.Fatal(err)
				}
				lp.slab.free(c.out)
				c.out = chunkRef{idx: noChunk}
				r.st.pending = 0
			}
		})
	}
}
