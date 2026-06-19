package clock

import (
	"testing"
	"time"
)

// Package-level sinks defeat the compiler's dead-store elimination so the
// benched calls cannot be optimised away.
var (
	sinkTime     time.Time
	sinkDuration time.Duration
)

// BenchmarkSystem_Now measures the per-call cost of the hot-path clock read:
// every logger record calls System.Now() exactly once, so this is the smallest
// yet most-called function in the SDK. It is a thin wrapper over time.Now, so
// the number is the baseline callers reason about (~time.Now itself, 0 allocs).
func BenchmarkSystem_Now(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: single wall-clock read — the per-record hot-path cost.
		sinkTime = System.Now()
	}
}

// BenchmarkSystem_Now_Parallel measures System.Now() under GOMAXPROCS>1 to
// confirm the stdlib wall-clock read introduces no contention when many
// goroutines read the clock concurrently (the multi-producer logger case).
func BenchmarkSystem_Now_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		//: every P reads the clock independently — no shared state to contend.
		for pb.Next() {
			sinkTime = System.Now()
		}
	})
}

// BenchmarkSystem_Since measures the per-call cost of the elapsed-duration
// wrapper over time.Since. The start instant is captured once before the loop
// so the number isolates the subtraction, not a second clock read.
func BenchmarkSystem_Since(b *testing.B) {
	start := System.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: monotonic-aware subtraction against a fixed start instant.
		sinkDuration = System.Since(start)
	}
}

// BenchmarkSystem_Since_Parallel measures System.Since() under GOMAXPROCS>1 to
// confirm the elapsed-duration read scales without contention across goroutines.
func BenchmarkSystem_Since_Parallel(b *testing.B) {
	start := System.Now()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		//: each P subtracts against the shared, read-only start instant.
		for pb.Next() {
			sinkDuration = System.Since(start)
		}
	})
}

// BenchmarkSystem_NowSincePair measures the typical usage shape: record a start
// instant then compute the elapsed duration. It documents the combined cost of
// the two reads a timing span pays end to end.
func BenchmarkSystem_NowSincePair(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: one read to open the span, one read+subtract to close it.
		start := System.Now()
		sinkDuration = System.Since(start)
	}
}
