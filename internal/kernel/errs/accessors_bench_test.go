package errs

import (
	"fmt"
	"testing"
)

// benchSentinel is a representative origin *Error: a sentinel with
// Code/Reason/Public/Private (no Fields — Define attaches none; fields are a
// Wrap/runtime concern). The Of-accessors walk a chain down to the deepest
// *Error, so a realistic fixture is a stdlib error wrapped over this sentinel.
var benchSentinel = Define(
	Pack(2, 3, 4, 5),
	"BENCH_SENTINEL",
	"bench public message",
	"bench private detail",
)

// errBenchChain is the error the accessors are benched against: the sentinel
// behind an fmt.Errorf("%w") wrapper, so each accessor exercises a real
// errors.AsType walk rather than a single direct assertion.
var errBenchChain = fmt.Errorf("outer context: %w", benchSentinel)

// errBenchFieldsChain is a separate fixture for the FieldsOf benchmarks: the
// sentinel Wrapped with one structured field, behind a stdlib wrapper. Because
// the underlying *Error actually carries a field, FieldsOf exercises the
// allocating defensive copy (slices.Clone of a non-empty slice) it is meant to
// quantify — errBenchChain's origin has no fields and would only hit the
// nil/empty fast path.
var errBenchFieldsChain = fmt.Errorf("outer context: %w", Wrap(
	benchSentinel,
	WrapParams{Code: Pack(2, 3, 4, 6), Reason: "BENCH_FIELDS", Public: "p", Private: "pr"},
	String("bench_key", "bench_val"),
))

// Per-result sinks keep the benched reads alive without cross-file collisions.
var (
	sinkOfCode   Code
	sinkOfBool   bool
	sinkOfString string
	sinkOfInt    int
	sinkOfFields []FieldValue
	sinkOfTrail  []Code
)

// BenchmarkCodeOf measures the typed Code accessor walking the wrap chain to
// the deepest *Error. It walks Unwrap without allocating, so it must be 0
// allocs/op.
func BenchmarkCodeOf(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: walk to the deepest *Error and read its Code.
		sinkOfCode, sinkOfBool = CodeOf(errBenchChain)
	}
}

// BenchmarkCodeOf_Parallel confirms CodeOf is goroutine-safe — the walk only
// reads immutable error state, so it must stay correct and alloc-free.
func BenchmarkCodeOf_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		//: each P walks the shared, immutable chain independently.
		for pb.Next() {
			sinkOfCode, sinkOfBool = CodeOf(errBenchChain)
		}
	})
}

// BenchmarkReasonOf measures the deepest-Reason accessor; pure chain walk,
// expect 0 allocs/op.
func BenchmarkReasonOf(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: read the deepest *Error's stable Reason identifier.
		sinkOfString, sinkOfBool = ReasonOf(errBenchChain)
	}
}

// BenchmarkReasonOf_Parallel confirms ReasonOf reads are contention-free.
func BenchmarkReasonOf_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		//: immutable read — no shared mutable state to contend on.
		for pb.Next() {
			sinkOfString, sinkOfBool = ReasonOf(errBenchChain)
		}
	})
}

// BenchmarkPublicOf measures the wire-safe message accessor; chain walk only,
// expect 0 allocs/op.
func BenchmarkPublicOf(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: return the deepest *Error's wire-safe Public string.
		sinkOfString = PublicOf(errBenchChain)
	}
}

// BenchmarkPublicOf_Parallel confirms PublicOf reads are contention-free.
func BenchmarkPublicOf_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		//: each P reads the shared chain's Public without mutation.
		for pb.Next() {
			sinkOfString = PublicOf(errBenchChain)
		}
	})
}

// BenchmarkPrivateOf measures the diagnostic-only message accessor; chain walk
// only, expect 0 allocs/op.
func BenchmarkPrivateOf(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: return the deepest *Error's log-only Private string.
		sinkOfString = PrivateOf(errBenchChain)
	}
}

// BenchmarkPrivateOf_Parallel confirms PrivateOf reads are contention-free.
func BenchmarkPrivateOf_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		//: immutable Private read across all P goroutines.
		for pb.Next() {
			sinkOfString = PrivateOf(errBenchChain)
		}
	})
}

// BenchmarkFieldsOf measures the structured-field collector. It returns a
// defensive copy of the field slice, so it MUST report allocs — the number
// quantifies the per-call copy cost consumers pay when reading fields.
func BenchmarkFieldsOf(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: collect a defensive copy of the chain's fields (slice allocation).
		sinkOfFields = FieldsOf(errBenchFieldsChain)
	}
}

// BenchmarkFieldsOf_Parallel measures the copying collector under contention;
// each P allocates its own copy, so the slice clone never aliases.
func BenchmarkFieldsOf_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		//: each P produces its own defensive copy — no aliasing across P.
		for pb.Next() {
			sinkOfFields = FieldsOf(errBenchFieldsChain)
		}
	})
}

// BenchmarkHTTPStatusOf measures the wire-status accessor; chain walk plus a
// direct override read, expect 0 allocs/op.
func BenchmarkHTTPStatusOf(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: walk to the deepest *Error and read its HTTP status.
		sinkOfInt = HTTPStatusOf(errBenchChain)
	}
}

// BenchmarkHTTPStatusOf_Parallel confirms the status accessor is read-only and
// contention-free.
func BenchmarkHTTPStatusOf_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		//: each P reads the shared chain's HTTP status independently.
		for pb.Next() {
			sinkOfInt = HTTPStatusOf(errBenchChain)
		}
	})
}

// BenchmarkExitCodeOf measures the POSIX exit-code accessor; chain walk plus a
// direct override read, expect 0 allocs/op.
func BenchmarkExitCodeOf(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: walk to the deepest *Error and read its exit code.
		sinkOfInt = ExitCodeOf(errBenchChain)
	}
}

// BenchmarkExitCodeOf_Parallel confirms the exit-code accessor is read-only and
// contention-free.
func BenchmarkExitCodeOf_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		//: each P reads the shared chain's exit code independently.
		for pb.Next() {
			sinkOfInt = ExitCodeOf(errBenchChain)
		}
	})
}

// BenchmarkHasReason measures the chain-walking Reason matcher on a hit at the
// deepest layer. Pure walk + string compare, expect 0 allocs/op.
func BenchmarkHasReason(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: search the chain for the deepest *Error's Reason (a guaranteed hit).
		sinkOfBool = HasReason(errBenchChain, "BENCH_SENTINEL")
	}
}

// BenchmarkHasReason_Parallel confirms HasReason walks are contention-free.
func BenchmarkHasReason_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		//: each P walks the shared chain matching the Reason independently.
		for pb.Next() {
			sinkOfBool = HasReason(errBenchChain, "BENCH_SENTINEL")
		}
	})
}

// BenchmarkTrailOf measures the wrap-trail accessor. It returns a defensive
// copy of the trail slice, so it reports allocs when the trail is non-empty —
// here the fixture is a wrapped *Error carrying a one-entry trail.
func BenchmarkTrailOf(b *testing.B) {
	wrapped := Wrap(benchSentinel, WrapParams{Code: Pack(2, 3, 4, 6), Reason: "BENCH_WRAP", Public: "wrapped", Private: "wrapped detail"})
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: copy out the deepest *Error's wrap-trail codes.
		sinkOfTrail = TrailOf(wrapped)
	}
}

// BenchmarkTrailOf_Parallel measures the copying trail accessor under
// contention; each P clones its own slice.
func BenchmarkTrailOf_Parallel(b *testing.B) {
	wrapped := Wrap(benchSentinel, WrapParams{Code: Pack(2, 3, 4, 6), Reason: "BENCH_WRAP", Public: "wrapped", Private: "wrapped detail"})
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		//: each P clones the trail independently — no shared backing array.
		for pb.Next() {
			sinkOfTrail = TrailOf(wrapped)
		}
	})
}
