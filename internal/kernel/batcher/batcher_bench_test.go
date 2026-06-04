package batcher

import (
	"context"
	"testing"
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
