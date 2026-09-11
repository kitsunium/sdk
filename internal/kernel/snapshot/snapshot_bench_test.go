package snapshot_test

import (
	"maps"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// the table this container is built for: a read-mostly map published whole.
type table struct {
	m map[string]int
}

func newTable(n int) *table {
	t := &table{m: make(map[string]int, n)}
	for i := range n {
		t.m[string(rune('a'+i%26))+string(rune('a'+i/26))] = i
	}
	return t
}

// package-level sinks so the compiler cannot prove the loads dead.
// parallelSink is the one a RunParallel body writes: every worker stores its
// last load when it finishes, concurrently, so a plain variable there is a data
// race the race detector reports whenever the benchmarks run under -race.
var (
	tableSink    *table
	intSink      int
	parallelSink atomic.Pointer[table]
)

// BenchmarkLoad is the number the whole package exists for: a single
// atomic.Pointer load, no lock, no allocation. Every other cost in this file
// is paid by writers, and the design bets that writers are rare.
func BenchmarkLoad(b *testing.B) {
	v := snapshot.NewValue(newTable(64))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		tableSink = v.Load()
	}
}

// BenchmarkLoad_MutexBaseline is the alternative this primitive replaces: the
// same read guarded by an RWMutex. The delta is what copy-on-write buys on the
// read path, and it is the honest comparison — not "atomic vs nothing".
func BenchmarkLoad_MutexBaseline(b *testing.B) {
	var mu sync.RWMutex
	cur := newTable(64)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		mu.RLock()
		tableSink = cur
		mu.RUnlock()
	}
}

// BenchmarkLoad_Parallel is the property that decides between the two: readers
// never take a lock, so throughput should scale with cores rather than
// collapse. Compare against Load_ParallelMutexBaseline below.
func BenchmarkLoad_Parallel(b *testing.B) {
	v := snapshot.NewValue(newTable(64))
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var local *table
		for pb.Next() {
			local = v.Load()
		}
		parallelSink.Store(local)
	})
}

func BenchmarkLoad_ParallelMutexBaseline(b *testing.B) {
	var mu sync.RWMutex
	cur := newTable(64)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var local *table
		for pb.Next() {
			mu.RLock()
			local = cur
			mu.RUnlock()
		}
		parallelSink.Store(local)
	})
}

// BenchmarkLoadAndRead is the realistic reader: load the snapshot AND read
// through it. It exists so the Load number above is not mistaken for the cost
// of using the data — the map lookup dominates, which is the point.
func BenchmarkLoadAndRead(b *testing.B) {
	v := snapshot.NewValue(newTable(64))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		intSink = v.Load().m["aa"]
	}
}

// BenchmarkStore, BenchmarkSwap and BenchmarkUpdate price the writer side.
// All three take the mutex; none of them copies anything, because copying is
// the CALLER's job — which is the trade the container makes and the reason
// these numbers look cheap next to Update_WithClone below.
func BenchmarkStore(b *testing.B) {
	v := snapshot.NewValue(newTable(64))
	next := newTable(64)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		v.Store(next)
	}
}

func BenchmarkSwap(b *testing.B) {
	v := snapshot.NewValue(newTable(64))
	next := newTable(64)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		tableSink = v.Swap(next)
	}
}

func BenchmarkUpdate_NoOp(b *testing.B) {
	v := snapshot.NewValue(newTable(64))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: returning the current pointer unchanged is the documented way for
		//: fn to abort a conditional update; this is its floor cost.
		v.Update(func(cur *table) *table { return cur })
	}
}

// BenchmarkUpdate_WithClone is what a real write costs, and it is the number a
// caller must size against: copy-on-write means every mutation rebuilds the
// whole table. At 64 entries that is already three orders of magnitude above a
// Load — which is exactly why the container is for READ-MOSTLY state and says
// so in its own doc comment.
func BenchmarkUpdate_WithClone(b *testing.B) {
	v := snapshot.NewValue(newTable(64))
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		v.Update(func(cur *table) *table {
			next := &table{m: make(map[string]int, len(cur.m)+1)}
			maps.Copy(next.m, cur.m)
			next.m["zz"] = i
			return next
		})
		i++
	}
}

// BenchmarkLoad_UnderWriter is the question a lock-free read path is chosen to
// answer: does a concurrent writer stall readers? One goroutine publishes as
// fast as it can while the timed loop reads.
func BenchmarkLoad_UnderWriter(b *testing.B) {
	v := snapshot.NewValue(newTable(64))
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		a, c := newTable(64), newTable(64)
		for {
			select {
			case <-stop:
				return
			default:
				v.Store(a)
				v.Store(c)
			}
		}
	})
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		tableSink = v.Load()
	}
	b.StopTimer()
	close(stop)
	wg.Wait()
}
