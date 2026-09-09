package clock

import (
	"testing"
	"time"
)

// benchWaits is the number of armed waits the Advance benchmarks hold. Eight is
// the shape a realistic test double carries — a handful of timeouts and a
// ticker or two — not a scheduler's worth.
const benchWaits int = 8

// Package-level sinks defeat the compiler's dead-store elimination so the
// benched calls cannot be optimised away.
var (
	sinkTime     time.Time
	sinkDuration time.Duration
	sinkTimer    Timer
	sinkTicker   Ticker
	sinkInt      int
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

// BenchmarkStdlib_NewTimer is the CONTROL for BenchmarkSystem_NewTimer: raw
// time.NewTimer with no port in the way. The pair is what makes the wrapper's
// cost readable — the claim "adapting *time.Timer to the Timer interface adds
// no allocation" is only worth anything next to the unwrapped number.
func BenchmarkStdlib_NewTimer(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: Stop keeps the live-timer set bounded at one.
		tm := time.NewTimer(time.Hour)
		tm.Stop()
	}
}

// BenchmarkSystem_NewTimer measures the same allocation through the port. The
// systemTimer wrapper holds exactly one pointer, so boxing it into the Timer
// interface is pointer-shaped and allocation-free; any delta against the
// control above is the cost of the indirection, not of a second allocation.
func BenchmarkSystem_NewTimer(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: construct through the interface, then stop to bound the live set.
		tm := System.NewTimer(time.Hour)
		tm.Stop()
		sinkTimer = tm
	}
}

// BenchmarkSystem_NewTicker measures ticker construction through the port,
// including the requirePositivePeriod check that guards the zero-period case.
func BenchmarkSystem_NewTicker(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: Stop keeps the live-ticker set bounded at one.
		tk := System.NewTicker(time.Hour)
		tk.Stop()
		sinkTicker = tk
	}
}

// BenchmarkManual_Now measures the manual clock's read against
// BenchmarkSystem_Now. It is a mutex acquire plus a struct copy where System is
// a bare syscall-free wall read, so the two numbers answer "what does a test
// pay to make time deterministic" — the answer must stay small enough that a
// ManualClock is usable inside a benchmark, not only inside a test.
func BenchmarkManual_Now(b *testing.B) {
	m := NewManualClock(time.Unix(0, 0).UTC())
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: lock, copy the instant, unlock.
		sinkTime = m.Now()
	}
}

// BenchmarkManual_NewTimer measures registering one wait: a channel, a wait
// record, and an append to the armed set.
func BenchmarkManual_NewTimer(b *testing.B) {
	m := NewManualClock(time.Unix(0, 0).UTC())
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: Stop disarms it again so the armed set does not grow with b.N.
		tm := m.NewTimer(time.Hour)
		tm.Stop()
		sinkTimer = tm
	}
}

// BenchmarkManual_Advance_Idle measures the cost of an Advance that fires
// nothing while benchWaits waits are armed — the due scan alone. It is the
// common case in a test that steps time in small increments.
func BenchmarkManual_Advance_Idle(b *testing.B) {
	m := NewManualClock(time.Unix(0, 0).UTC())
	//: hour-long deadlines never come due during the run.
	for range benchWaits {
		sinkTimer = m.NewTimer(time.Hour)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: a nanosecond step: full scan, no fire.
		m.Advance(time.Nanosecond)
	}
	sinkInt = m.Pending()
}

// BenchmarkManual_Advance_Firing measures an Advance that fires and re-arms
// every armed wait: benchWaits tickers, one period per iteration. This is the
// worst realistic shape, and the number that bounds a driving loop's cost.
func BenchmarkManual_Advance_Firing(b *testing.B) {
	m := NewManualClock(time.Unix(0, 0).UTC())
	//: tickers survive their tick, so the armed set stays at benchWaits.
	for range benchWaits {
		sinkTicker = m.NewTicker(time.Second)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: one full period: every ticker fires once and re-arms.
		m.Advance(time.Second)
	}
	sinkInt = m.Pending()
}
