package ring

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

// BenchmarkNew measures Queue construction. New allocates the queueRing struct
// plus its cap+1 slots backing array, so ~1 alloc is expected — this number is
// the floor for any caller that builds rings on a hot path and the regression
// guard if the constructor ever grows a hidden allocation.
func BenchmarkNew(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: build a fresh ring each iteration so the alloc count is per-construction.
		q, err := New[int](1024)
		if err != nil {
			b.Fatalf("New: %v", err)
		}
		//: assert on the result so the compiler cannot elide the make.
		if q == nil {
			b.Fatal("New returned a nil queue")
		}
	}
}

// BenchmarkTryWrite_Happy measures the single-producer, non-saturated TryWrite
// path. SPSC is honoured: exactly one goroutine writes. The ring is kept from
// saturating by draining one slot every iteration, so each TryWrite lands on the
// fast path (two atomic loads, one slot store, one atomic store). 0 allocs
// expected — the slot store reuses the backing array.
func BenchmarkTryWrite_Happy(b *testing.B) {
	q, err := New[int](1024)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	//: pre-load one item so the very first TryRead in the loop always succeeds.
	if err := q.TryWrite(0); err != nil {
		b.Fatalf("seed TryWrite: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		//: write then immediately drain so the ring never saturates and every
		//: TryWrite stays on the non-Full fast path.
		if err := q.TryWrite(i); err != nil {
			b.Fatalf("TryWrite: %v", err)
		}
		if _, err := q.TryRead(); err != nil {
			b.Fatalf("drain TryRead: %v", err)
		}
	}
}

// BenchmarkTryRead_Happy measures the single-consumer, non-empty TryRead path.
// SPSC is honoured: exactly one goroutine reads. The ring is replenished one
// slot every iteration so each TryRead lands on the fast path (two atomic loads,
// one slot load, one zero-store, one atomic store). 0 allocs expected.
func BenchmarkTryRead_Happy(b *testing.B) {
	q, err := New[int](1024)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		//: replenish then drain so the ring never empties and every TryRead stays
		//: on the non-Empty fast path.
		if err := q.TryWrite(i); err != nil {
			b.Fatalf("replenish TryWrite: %v", err)
		}
		if _, err := q.TryRead(); err != nil {
			b.Fatalf("TryRead: %v", err)
		}
	}
}

// BenchmarkTryWrite_Full measures the saturated TryWrite path: the ring is
// filled to capacity before the loop, so every TryWrite hits the wrap-check,
// finds next == head, and returns the pre-allocated Full sentinel. 0 allocs
// expected because Full is a package-level var, not a constructed error.
func BenchmarkTryWrite_Full(b *testing.B) {
	q, err := New[int](8)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	//: fill the ring to capacity so every benchmarked TryWrite returns Full.
	for q.TryWrite(0) == nil {
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: the ring is saturated — this must return the Full sentinel with no alloc.
		if err := q.TryWrite(1); !errors.Is(err, Full) {
			b.Fatalf("TryWrite on full ring: got %v, want Full", err)
		}
	}
}

// BenchmarkTryRead_Empty measures the empty TryRead path: the ring is never
// written, so every TryRead finds head == tail and returns the pre-allocated
// Empty sentinel plus the zero value. 0 allocs expected — Empty is a
// package-level var and the zero value is a stack value.
func BenchmarkTryRead_Empty(b *testing.B) {
	q, err := New[int](8)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: the ring is empty — this must return the Empty sentinel with no alloc.
		if _, err := q.TryRead(); !errors.Is(err, Empty) {
			b.Fatalf("TryRead on empty ring: got %v, want Empty", err)
		}
	}
}

// BenchmarkCapacity measures the immutable Capacity field read. It is a single
// widening of the stored cap; 0 allocs and a handful of ns/op are expected. The
// result is consumed via a sink var so the read cannot be optimised away.
func BenchmarkCapacity(b *testing.B) {
	q, err := New[int](1024)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	var sink int
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: accumulate into a sink so the compiler keeps the Capacity call.
		sink += q.Capacity()
	}
	//: assert the sink accumulated so the Capacity reads cannot be elided.
	if sink == 0 {
		b.Fatal("Capacity sink stayed zero")
	}
}

// BenchmarkLen measures the Len snapshot: two atomic loads, a subtract, and a
// modulo. 0 allocs expected. The ring is half-filled so the modulo arithmetic
// runs on a non-zero span rather than the trivial empty case.
func BenchmarkLen(b *testing.B) {
	q, err := New[int](1024)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	//: half-fill the ring so Len exercises the wrap-around subtract, not Len==0.
	for i := range 512 {
		if err := q.TryWrite(i); err != nil {
			b.Fatalf("seed TryWrite: %v", err)
		}
	}
	var sink int
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: accumulate into a sink so the compiler keeps the Len call.
		sink += q.Len()
	}
	//: assert the sink accumulated so the Len reads cannot be elided.
	if sink == 0 {
		b.Fatal("Len sink stayed zero")
	}
}

// BenchmarkSCStorePair is the floor of the two happy rows: the two
// sequentially consistent stores an iteration of TryWrite_Happy or
// TryRead_Happy cannot do without — tail's publication of the slot and head's
// release of it — on two padded words, and nothing else. sync/atomic offers no
// weaker store (all its operations are sequentially consistent), and on amd64
// such a store is an XCHG, a locked instruction that drains the store buffer.
// The happy rows are read against this one, on the same box (#1034).
func BenchmarkSCStorePair(b *testing.B) {
	var words struct {
		tail atomic.Uint64
		_    linePad
		head atomic.Uint64
	}
	b.ReportAllocs()
	for i := uint64(0); b.Loop(); i++ {
		//: what TryWrite publishes, then what TryRead releases.
		words.tail.Store(i + 1)
		words.head.Store(i + 1)
	}
}

// BenchmarkSPSC_ProducerConsumer measures the end-to-end SPSC throughput with
// ONE dedicated producer goroutine and ONE dedicated consumer goroutine — the
// only topology the ring is contractually safe under.
//
// This deliberately does NOT use b.RunParallel. RunParallel spawns GOMAXPROCS
// worker goroutines all running the same body; here that would mean N concurrent
// producers AND N concurrent consumers, which violates the SPSC invariant
// (exactly one TryWrite-er and exactly one TryRead-er at any instant) and would
// be a data race on head/tail/slots. Instead we hand-roll the two-goroutine
// pattern: the producer spins on TryWrite (re-trying on Full), the consumer
// spins on TryRead (re-trying on Empty), and b.N records flow end-to-end across
// the lock-free buffer. The sub-benchmarks sweep ring sizes 4, 64, 1024, 65536
// so the throughput-vs-buffer-depth curve (and any false-sharing cliff) is
// visible. ns/op here is per-record wall time across the full producer→consumer
// handoff, not a single TryWrite.
func BenchmarkSPSC_ProducerConsumer(b *testing.B) {
	for _, size := range []int{4, 64, 1024, 65536} {
		//: pass size as an argument so the sub-bench body does not capture the
		//: loop variable by closure (no heap escape per iteration).
		b.Run(sizeName(size), func(b *testing.B) { benchSPSC(b, size) })
	}
}

// benchSPSC runs the two-goroutine SPSC throughput body for one ring size. It is
// a free function (not a closure) so size arrives as a value argument rather
// than a captured loop variable.
func benchSPSC(b *testing.B, size int) {
	q, err := New[int](size)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	n := b.N
	var wg sync.WaitGroup
	wg.Add(2)
	b.ReportAllocs()
	b.ResetTimer()
	//: dedicated PRODUCER — the sole TryWrite-er. Spin-retry on Full so a
	//: slow consumer back-pressures instead of dropping records.
	go func() {
		defer wg.Done()
		for i := range n {
			for q.TryWrite(i) != nil {
			}
		}
	}()
	//: dedicated CONSUMER — the sole TryRead-er. Spin-retry on Empty until
	//: all n records have crossed the buffer.
	go func() {
		defer wg.Done()
		for range n {
			for {
				if _, err := q.TryRead(); err == nil {
					break
				}
			}
		}
	}()
	wg.Wait()
}

// sizeName renders a ring size as a stable sub-benchmark label.
func sizeName(size int) string {
	switch size {
	case 4:
		return "size4"
	case 64:
		return "size64"
	case 1024:
		return "size1024"
	default:
		return "size65536"
	}
}

// BenchmarkMPSC_Producers measures the MPSC sibling end to end: N producer
// goroutines share b.N records and ONE consumer reads them all, through a
// 1024-slot NewMulti ring. ns/op is per-record wall time across the hand-off,
// spin-retries on Full and Empty included — the row #805's acceptance reads
// beside SPSC_ProducerConsumer/size1024 (Producers1 is the same topology as
// that row, through the MPSC claim instead of the SPSC store). A writer that
// finds the ring full, and the reader that finds it empty, yield
// (runtime.Gosched) before trying again, as the mutex row's do.
func BenchmarkMPSC_Producers(b *testing.B) {
	for _, producers := range []int{1, 4, 8} {
		b.Run(producersName(producers), func(b *testing.B) { benchMPSC(b, producers) })
	}
}

// benchMPSC runs producers writers and one reader over b.N records.
func benchMPSC(b *testing.B, producers int) {
	q, err := NewMulti[int](1024)
	if err != nil {
		b.Fatalf("NewMulti: %v", err)
	}
	n := b.N
	var wg sync.WaitGroup
	b.ReportAllocs()
	b.ResetTimer()
	for p := range producers {
		//: each writer's share; the first takes the remainder.
		share := n / producers
		if p == 0 {
			share += n % producers
		}
		wg.Go(func() {
			for i := range share {
				//: a full ring yields, as a producer with somewhere else to be does.
				for q.TryWrite(i) != nil {
					runtime.Gosched()
				}
			}
		})
	}
	//: the one reader, until every record has crossed; an empty ring yields.
	for read := 0; read < n; {
		if _, rerr := q.TryRead(); rerr == nil {
			read++
		} else {
			runtime.Gosched()
		}
	}
	wg.Wait()
}

// BenchmarkMutexSPSC_Producers is the shape the MPSC sibling replaces: the
// SPSC ring behind one mutex that every producer and the consumer take, as
// the async logger middleware does (#805). Same topology and record count as
// MPSC_Producers, so the two rows read side by side.
func BenchmarkMutexSPSC_Producers(b *testing.B) {
	for _, producers := range []int{1, 4, 8} {
		b.Run(producersName(producers), func(b *testing.B) { benchMutexSPSC(b, producers) })
	}
}

// benchMutexSPSC runs producers writers and one reader over b.N records
// through a mutex-guarded SPSC ring.
func benchMutexSPSC(b *testing.B, producers int) {
	q, err := New[int](1024)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	var mu sync.Mutex
	try := func(i int) error {
		mu.Lock()
		defer mu.Unlock()
		return q.TryWrite(i)
	}
	n := b.N
	var wg sync.WaitGroup
	b.ReportAllocs()
	b.ResetTimer()
	for p := range producers {
		share := n / producers
		if p == 0 {
			share += n % producers
		}
		wg.Go(func() {
			for i := range share {
				for try(i) != nil {
					runtime.Gosched()
				}
			}
		})
	}
	for read := 0; read < n; {
		mu.Lock()
		_, rerr := q.TryRead()
		mu.Unlock()
		if rerr == nil {
			read++
		} else {
			runtime.Gosched()
		}
	}
	wg.Wait()
}

// BenchmarkMPSC_TryWrite_Happy is the uncontended claim: one goroutine writes
// then drains, as TryWrite_Happy does on the SPSC ring.
func BenchmarkMPSC_TryWrite_Happy(b *testing.B) {
	q, err := NewMulti[int](1024)
	if err != nil {
		b.Fatalf("NewMulti: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		if err := q.TryWrite(i); err != nil {
			b.Fatalf("TryWrite: %v", err)
		}
		if _, err := q.TryRead(); err != nil {
			b.Fatalf("drain TryRead: %v", err)
		}
	}
}

// producersName renders a producer count as a stable sub-benchmark label.
func producersName(producers int) string {
	switch producers {
	case 1:
		return "Producers1"
	case 4:
		return "Producers4"
	default:
		return "Producers8"
	}
}
