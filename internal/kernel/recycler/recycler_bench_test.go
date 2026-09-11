package recycler_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/recycler"
)

// sinks defeat dead-code elimination. A pooled value that is never observed
// can be proven unused, and the compiler would then measure nothing.
var (
	byteSink  []byte
	ptrSink   *[]byte
	smallSink *small
)

// small is the object a caller is TEMPTED to pool: cheap, fixed-size, and with
// no backing array worth reusing.
type small struct {
	a, b, c, d int64
}

// BenchmarkPool_GetPut_Small vs BenchmarkNew_Small is the comparison that
// decides whether this package should be used at all for a given type. Pooling
// is not free: sync.Pool costs a per-P lookup and an interface box, and for a
// small object that price can exceed the allocation it replaces.
func BenchmarkPool_GetPut_Small(b *testing.B) {
	p := recycler.NewPool(func() *small { return &small{} })
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		v := p.Get()
		v.a++
		smallSink = v
		p.Put(v)
	}
}

func BenchmarkNew_Small(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		v := &small{}
		v.a++
		smallSink = v
	}
}

// BenchmarkPool_GetPut_Buffer4K vs BenchmarkNew_Buffer4K is the same comparison
// for the case the package was written for: a value whose backing array is the
// expensive part. Here the pool has something real to recycle.
func BenchmarkPool_GetPut_Buffer4K(b *testing.B) {
	p := recycler.NewPool(func() []byte { return make([]byte, 0, 4096) })
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		buf := p.Get()
		buf = append(buf[:0], 'x')
		byteSink = buf
		p.Put(buf)
	}
}

func BenchmarkNew_Buffer4K(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		buf := make([]byte, 0, 4096)
		buf = append(buf, 'x')
		byteSink = buf
	}
}

// BenchmarkPool_GetPut_Buffer64K repeats it an order of magnitude up, where the
// allocation is large enough to be served off-heap-span and the pool's edge
// widens further.
func BenchmarkPool_GetPut_Buffer64K(b *testing.B) {
	p := recycler.NewPool(func() []byte { return make([]byte, 0, 65536) })
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		buf := p.Get()
		buf = append(buf[:0], 'x')
		byteSink = buf
		p.Put(buf)
	}
}

func BenchmarkNew_Buffer64K(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		buf := make([]byte, 0, 65536)
		buf = append(buf, 'x')
		byteSink = buf
	}
}

// BenchmarkPool_Parallel is the property sync.Pool exists for: per-P shards, so
// contention should NOT scale with goroutine count the way a mutex-guarded free
// list would.
func BenchmarkPool_Parallel(b *testing.B) {
	p := recycler.NewPool(func() []byte { return make([]byte, 0, 4096) })
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := p.Get()
			buf = append(buf[:0], 'x')
			p.Put(buf)
		}
	})
}

// BenchmarkPool_GetPut_BufferPtr4K is the SAME workload as Buffer4K with one
// difference — the pool holds `*[]byte` rather than `[]byte` — and the delta is
// the single most important number in this file.
//
// sync.Pool stores `any`. A pointer fits in an interface word and is boxed for
// free; a slice header is three words and cannot be, so every Put of a bare
// slice heap-allocates 24 B to carry it. The package doc's claim that callers
// recycle "without paying the boxing cost" is therefore true ONLY for
// pointer-shaped T, and this pair of benchmarks is what makes that concrete.
//
// Every consumer in this repository already does the right thing —
// kernel/buffer pools *[]byte, core/codec/scratch pools *bytes.Buffer and
// *bytes.Reader, service/logger pools *chainBuilder, async pools *recordEntry,
// net/server pools *pooledConn — so this exists to keep the next one honest.
func BenchmarkPool_GetPut_BufferPtr4K(b *testing.B) {
	p := recycler.NewPool(func() *[]byte { return new(make([]byte, 0, 4096)) })
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bp := p.Get()
		*bp = append((*bp)[:0], 'x')
		ptrSink = bp
		p.Put(bp)
	}
}

// BenchmarkPool_ParallelPtr is Pool_Parallel with the same correction, so the
// contention number is read without the boxing allocation on top of it.
func BenchmarkPool_ParallelPtr(b *testing.B) {
	p := recycler.NewPool(func() *[]byte { return new(make([]byte, 0, 4096)) })
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			bp := p.Get()
			*bp = append((*bp)[:0], 'x')
			p.Put(bp)
		}
	})
}

// BenchmarkCappedPool_GetPut_UnderCap prices the CappedPool's happy path: the
// cap check, the reset, then the plain Put. The delta against
// Pool_GetPut_Buffer4K is what the reset-and-discard policy costs.
func BenchmarkCappedPool_GetPut_UnderCap(b *testing.B) {
	p := recycler.NewCappedPool(
		func() []byte { return make([]byte, 0, 4096) },
		func(v []byte) { _ = v[:0] },
		func(v []byte) int { return cap(v) },
		8192,
	)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		buf := p.Get()
		buf = append(buf[:0], 'x')
		byteSink = buf
		p.Put(buf)
	}
}

// BenchmarkCappedPool_GetPut_OverCap is the pathological configuration, and it
// is here to be measurable rather than discovered in production: every value
// exceeds maxCap, so every Put orphans and every Get allocates. A CappedPool
// whose threshold is below its own factory size is a plain allocator wearing a
// pool's name — this benchmark is what that looks like.
func BenchmarkCappedPool_GetPut_OverCap(b *testing.B) {
	p := recycler.NewCappedPool(
		func() []byte { return make([]byte, 0, 4096) },
		func(v []byte) { _ = v[:0] },
		func(v []byte) int { return cap(v) },
		64,
	)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		buf := p.Get()
		buf = append(buf[:0], 'x')
		byteSink = buf
		p.Put(buf)
	}
}
