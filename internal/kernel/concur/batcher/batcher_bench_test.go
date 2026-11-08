package batcher

import (
	"context"
	"testing"
	"time"
)

// BenchmarkAdd_CapFlush measures the producer-visible Add cost under a small
// MaxItems cap, so nearly every Add triggers an eager flush through the
// serialized deliver path (V6). The Sink is a no-op, so the number isolates the
// batcher's append + cap-check + deliverMu acquire — the serialization overhead
// the V6 fix adds. Single-producer: the mutex is uncontended here.
func BenchmarkAdd_CapFlush(b *testing.B) {
	bat := NewBatcher(func(context.Context, []int) error { return nil }, Config[int]{MaxItems: 16})
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		//: drive the cap-flush path so deliverMu is exercised every 16 Adds.
		if err := bat.Add(ctx, i); err != nil {
			b.Fatalf("Add: %v", err)
		}
	}
}

// BenchmarkAdd_Contended measures the same cap-flush path under concurrent
// producers, so deliverMu is contended (the worst case for the V6
// serialization). It quantifies how much the dedicated delivery mutex costs when
// many producers race into the Sink — the throughput figure the plan asks to
// compare against the unserialized baseline.
func BenchmarkAdd_Contended(b *testing.B) {
	bat := NewBatcher(func(context.Context, []int) error { return nil }, Config[int]{MaxItems: 16})
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		//: each P races the others into the serialized deliver path.
		var i int
		for pb.Next() {
			if err := bat.Add(ctx, i); err != nil {
				b.Errorf("Add: %v", err)
			}
			i++
		}
	})
}

// BenchmarkFlush_Empty measures the no-op Flush fast path (empty buffer): it
// still acquires and releases mu but never reaches deliverBatch, so it serves as
// the serialization-free baseline the cap-flush benches are read against.
func BenchmarkFlush_Empty(b *testing.B) {
	bat := NewBatcher(func(context.Context, []int) error { return nil }, Config[int]{})
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: an empty Flush short-circuits before the deliver mutex.
		if err := bat.Flush(ctx); err != nil {
			b.Fatalf("Flush: %v", err)
		}
	}
}

// BenchmarkAdd_CapFlush_SlowSink is #817's row: a 1 ms Sink — a network round
// trip's order — at MaxItems 16. Under FlushCaller, the zero mode, every 16th
// Add runs the Sink on the producer's goroutine, so the producer pays at
// least 1 ms / 16 ≈ 62 µs per Add on average; under FlushBackground the
// batcher's own goroutine runs it and the producer pays the append, until it
// outruns the Sink by two batches and waits for room. The producer works in
// bursts of two batches with the clock stopped between them, so the default
// -benchtime would wait out ~2 ms of Sink per 32 timed Adds: run it with a
// fixed count, -bench=SlowSink -benchtime=3200x (100 bursts).
func BenchmarkAdd_CapFlush_SlowSink(b *testing.B) {
	for _, mode := range []struct {
		name string
		on   FlushMode
	}{{"Caller", FlushCaller}, {"Background", FlushBackground}} {
		b.Run(mode.name, func(b *testing.B) { benchSlowSink(b, mode.on) })
	}
}

// benchSlowSink measures one producer's Add against a 1 ms Sink in mode on,
// over a bounded number of Adds so the Sink's backlog stays within reach.
func benchSlowSink(b *testing.B, on FlushMode) {
	slow := func(context.Context, []int) error {
		time.Sleep(time.Millisecond)
		return nil
	}
	bat := NewBatcher(slow, Config[int]{MaxItems: 16, FlushOn: on})
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		if err := bat.Add(ctx, i); err != nil {
			b.Fatalf("Add: %v", err)
		}
		//: one burst of 32 Adds — two batches — then the producer stops timing
		//: while the Sink catches up, as a producer between bursts does.
		if i%32 == 31 {
			b.StopTimer()
			if err := bat.Flush(ctx); err != nil {
				b.Fatalf("Flush: %v", err)
			}
			b.StartTimer()
		}
	}
	b.StopTimer()
	if err := bat.Close(ctx); err != nil {
		b.Fatalf("Close: %v", err)
	}
}
