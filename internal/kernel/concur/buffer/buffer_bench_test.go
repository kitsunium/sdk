package buffer

import "testing"

// BenchmarkGet_Steadystate measures the cost of Get once the pool is warm. The
// loop returns each borrowed buffer immediately so every subsequent Get hits a
// recycled *[]byte rather than the factory — this is the documented hot-path
// contract (0 allocs/op after warm-up). A non-zero allocs/op here means a
// recycled buffer is no longer being handed back and is a regression.
func BenchmarkGet_Steadystate(b *testing.B) {
	//: prime the pool so the very first benched Get already has a recycled value.
	Put(Get())
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		buf := Get()
		//: return it at once so the next Get recycles instead of allocating.
		Put(buf)
	}
}

// BenchmarkGetPut_RoundTrip measures the canonical call-site pattern: borrow a
// buffer, write into it, return it. The write touches the backing array so the
// bench reflects real scratch use rather than an empty borrow/return. Steady
// state is 0 allocs/op — the round trip never escapes to the GC once warm.
//
// There is deliberately no isolated BenchmarkPut: Put cannot be measured on its
// own for a sync.Pool-backed recycler. Re-inserting a pointer every iteration
// without a paired Get grows the pool's per-P shared slice until the next GC,
// so the reported ns/op and B/op describe pool growth, not the cost of a Put.
// The round trip is the only honest measurement of the Put half.
func BenchmarkGetPut_RoundTrip(b *testing.B) {
	//: warm the pool so the first round trip already recycles.
	Put(Get())
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		buf := Get()
		//: emulate a real scratch write so the backing array is actually used.
		*buf = append(*buf, 'x')
		Put(buf)
	}
}

// BenchmarkGetPut_Parallel measures the round trip under contention via
// RunParallel, where sync.Pool's per-P cache shines: each P keeps a private
// buffer so Get/Put stay lock-free and 0-alloc on the steady-state path. The
// number documents how the pool scales when many goroutines borrow and return
// concurrently — the worst case the per-P cache is designed for.
func BenchmarkGetPut_Parallel(b *testing.B) {
	//: warm the pool before fanning out so no P pays the cold-start factory.
	Put(Get())
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		//: each P borrows and returns against its private per-P cache.
		for pb.Next() {
			buf := Get()
			*buf = append(*buf, 'x')
			Put(buf)
		}
	})
}

// BenchmarkGetSize_16K is a 16 KiB borrow and its return in steady state:
// the class's own buffer, recycled — #810's acceptance reads 0 allocs/op.
func BenchmarkGetSize_16K(b *testing.B) {
	Put(GetSize(16 << 10))
	b.ReportAllocs()
	for b.Loop() {
		Put(GetSize(16 << 10))
	}
}

// BenchmarkMixedConsumers interleaves a 1 KiB consumer (a log line) and a
// 16 KiB one (a server read buffer) on the shared pool: each takes its own
// class, so neither is handed the other's buffer — the 16 KiB consumer
// never allocates for a 1 KiB buffer it was given, the 1 KiB one never pins
// a 16 KiB buffer.
func BenchmarkMixedConsumers(b *testing.B) {
	Put(Get())
	Put(GetSize(16 << 10))
	b.ReportAllocs()
	for b.Loop() {
		line := Get()
		*line = append(*line, "a log line"...)
		read := GetSize(16 << 10)
		*read = (*read)[:16<<10]
		Put(read)
		Put(line)
	}
}
