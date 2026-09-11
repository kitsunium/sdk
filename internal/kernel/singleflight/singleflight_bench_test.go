package singleflight_test

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/singleflight"
)

// BenchmarkDo_Uncontended is the price of a call that dedupes NOTHING: one
// goroutine, one key at a time, an fn that returns immediately. It is the
// floor the primitive can never go below, because every leading call pays for
// a goroutine, a derived context and a map insert + delete.
func BenchmarkDo_Uncontended(b *testing.B) {
	var group singleflight.Group[string, int]
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, _, err := group.Do(ctx, "k", func(context.Context) (int, error) { return 1, nil }); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDo_SharedKeyParallel is the realistic shape: every P goroutine asks
// for the SAME key. Most calls join an in-flight one and pay only a mutex
// acquisition plus a channel receive; the rest lead. The ratio of the two is
// what the primitive exists to change.
func BenchmarkDo_SharedKeyParallel(b *testing.B) {
	var group singleflight.Group[string, int]
	var runs atomic.Int64
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, _, err := group.Do(ctx, "k", func(context.Context) (int, error) {
				runs.Add(1)
				return 1, nil
			}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.StopTimer()
	//: the saving, reported rather than asserted: how many executions the
	//: b.N calls actually cost.
	b.ReportMetric(float64(runs.Load())/float64(b.N), "fn-runs/op")
}

// BenchmarkDo_DistinctKeysParallel is the worst case: nothing is ever shared,
// so every call pays the full leading price AND contends on the group mutex.
// A workload shaped like this should not use a Group at all, and the number
// here is what tells you so.
func BenchmarkDo_DistinctKeysParallel(b *testing.B) {
	var group singleflight.Group[string, int]
	var next atomic.Int64
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			key := strconv.FormatInt(next.Add(1), 10)
			if _, _, err := group.Do(ctx, key, func(context.Context) (int, error) { return 1, nil }); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkBaseline_DirectCall calls fn with no Group at all. Every number
// above is only meaningful against it: a Group is worth its overhead exactly
// when fn costs more than this difference AND callers collide.
func BenchmarkBaseline_DirectCall(b *testing.B) {
	ctx := b.Context()
	fn := func(context.Context) (int, error) { return 1, nil }
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := fn(ctx); err != nil {
			b.Fatal(err)
		}
	}
}
