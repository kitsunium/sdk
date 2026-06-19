package errs

import (
	"errors"
	"fmt"
	"testing"
)

// benchErrSentinel is the origin *Error the (*Error) method benches read from.
// Defined once so the per-method benches isolate the getter, not construction.
var benchErrSentinel = Define(
	Pack(3, 2, 4, 1),
	"BENCH_ERR",
	"bench err public",
	"bench err private",
)

// errBenchStdlibCause is a plain stdlib error used as the non-*Error cause in the
// Wrap-stdlib-path bench.
var errBenchStdlibCause = errors.New("bench stdlib cause")

// benchWrappedTrail is a wrapped *Error carrying a one-entry trail, used so the
// Error formatter and HasCode benches exercise the trail path, not just origin.
var benchWrappedTrail = Wrap(
	benchErrSentinel,
	WrapParams{Code: Pack(3, 2, 4, 2), Reason: "BENCH_WRAP_ERR", Public: "wrapped", Private: "wrapped detail"},
)

// Result sinks for the error.go surface.
var (
	sinkErr       *Error
	sinkErrError  error
	sinkErrCode   Code
	sinkErrString string
	sinkErrInt    int
	sinkErrBool   bool
	sinkErrFields []FieldValue
)

// BenchmarkDefine measures a single sentinel construction with no options. It
// allocates the *Error struct (bounded: 1 alloc, the heap *Error) — the number
// is the cold-path construction floor.
func BenchmarkDefine(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: build a fresh sentinel each iteration through the validating path.
		sinkErr = Define(Pack(3, 2, 4, 1), "BENCH_ERR", "bench err public", "bench err private")
	}
}

// BenchmarkDefine_WithOptions measures construction with two DefineOptions, so
// the bench captures the added opts-slice + closure-application cost over the
// bare Define floor.
func BenchmarkDefine_WithOptions(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: the variadic opts slice plus each closure apply is the extra cost.
		sinkErr = Define(
			Pack(3, 2, 4, 1), "BENCH_ERR", "bench err public", "bench err private",
			WithHTTPStatus(404), WithExitCode(2),
		)
	}
}

// BenchmarkNewError measures the tooling-friendly Define alias; it must track
// BenchmarkDefine within noise since it is a thin forward.
func BenchmarkNewError(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: NewError forwards verbatim to Define — benched to gate the alias cost.
		sinkErr = NewError(Pack(3, 2, 4, 1), "BENCH_ERR", "bench err public", "bench err private")
	}
}

// BenchmarkNewRuntime measures the non-panicking runtime constructor used by
// pkg/v1/errs.New. It validates, defensively clones an (empty) fields slice,
// and allocates the *Error — the external-consumer construction floor.
func BenchmarkNewRuntime(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: runtime-safe construction with validation but no panic path.
		sinkErr = NewRuntime(Pack(3, 2, 4, 1), "BENCH_ERR", "bench err public", "bench err private")
	}
}

// BenchmarkWrap_Stdlib measures Wrap when the cause is a plain stdlib error:
// params.Code becomes origin, the trail stays empty, and an *Error is
// allocated wrapping the cause. This is the case-5 path in Wrap.
func BenchmarkWrap_Stdlib(b *testing.B) {
	params := WrapParams{Code: Pack(3, 2, 4, 3), Reason: "BENCH_WRAP_STD", Public: "wrap public", Private: "wrap private"}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: stdlib-cause path — origin set from params, empty trail.
		sinkErr = Wrap(errBenchStdlibCause, params)
	}
}

// BenchmarkWrap_SDKCause measures Wrap when the cause is already an *Error:
// origin-wins inheritance plus a trail append (case-4). It allocates the new
// *Error and the grown trail slice — the hot-path wrap cost.
func BenchmarkWrap_SDKCause(b *testing.B) {
	params := WrapParams{Code: Pack(3, 2, 4, 4), Reason: "BENCH_WRAP_SDK", Public: "wrap public", Private: "wrap private"}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: origin-wins path — inherit metadata, append params.Code to the trail.
		sinkErr = Wrap(benchErrSentinel, params)
	}
}

// BenchmarkWrap_WithFields measures the SDK-cause wrap with two extra fields,
// so the bench captures the slices.Concat of inner fields with the new ones on
// top of the base wrap cost.
func BenchmarkWrap_WithFields(b *testing.B) {
	params := WrapParams{Code: Pack(3, 2, 4, 5), Reason: "BENCH_WRAP_F", Public: "wrap public", Private: "wrap private"}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: fields path — concat inner fields with the two wrap-site fields.
		sinkErr = Wrap(benchErrSentinel, params, String("k", "v"), Int("n", 7))
	}
}

// BenchmarkError_Code measures the direct origin-Code getter — a single field
// read, must be 0 allocs/op.
func BenchmarkError_Code(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: direct immutable field read.
		sinkErrCode = benchErrSentinel.Code()
	}
}

// BenchmarkError_Code_Parallel confirms the Code getter is a contention-free
// read.
func BenchmarkError_Code_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		//: immutable field — safe to read from every P.
		for pb.Next() {
			sinkErrCode = benchErrSentinel.Code()
		}
	})
}

// BenchmarkError_Reason measures the direct Reason getter — field read, 0
// allocs/op.
func BenchmarkError_Reason(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: direct read of the stable Reason identifier.
		sinkErrString = benchErrSentinel.Reason()
	}
}

// BenchmarkError_Public measures the direct Public getter — field read, 0
// allocs/op.
func BenchmarkError_Public(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: direct read of the wire-safe message.
		sinkErrString = benchErrSentinel.Public()
	}
}

// BenchmarkError_Private measures the direct Private getter — field read, 0
// allocs/op.
func BenchmarkError_Private(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: direct read of the diagnostic-only message.
		sinkErrString = benchErrSentinel.Private()
	}
}

// BenchmarkError_Fields measures the defensive-copy Fields getter. It clones
// the field slice, so it reports allocs — the per-read copy cost.
func BenchmarkError_Fields(b *testing.B) {
	withFields := Wrap(
		benchErrSentinel,
		WrapParams{Code: Pack(3, 2, 4, 7), Reason: "BENCH_FIELDS", Public: "p", Private: "pr"},
		String("k", "v"),
	)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: clone the internal field slice into a caller-owned copy.
		sinkErrFields = withFields.Fields()
	}
}

// BenchmarkError_HTTPStatus measures the override-or-default HTTP accessor — a
// branch on a field, 0 allocs/op.
func BenchmarkError_HTTPStatus(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: zero-vs-override branch then return; no allocation.
		sinkErrInt = benchErrSentinel.HTTPStatus()
	}
}

// BenchmarkError_ExitCode measures the override-or-default exit-code accessor —
// branch on a field, 0 allocs/op.
func BenchmarkError_ExitCode(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: same zero-vs-override discrimination as HTTPStatus.
		sinkErrInt = benchErrSentinel.ExitCode()
	}
}

// BenchmarkError_Error_NoTrail measures the string formatter on the no-trail
// fast path: a single concat of "[code REASON] public". It allocates the
// result string plus the Code.String() temporary — target ≤2 allocs/op.
func BenchmarkError_Error_NoTrail(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: no-trail concat path — the cheapest Error() rendering.
		sinkErrString = benchErrSentinel.Error()
	}
}

// BenchmarkError_Error_NoTrail_Parallel confirms the formatter reads only
// immutable state and is contention-free.
func BenchmarkError_Error_NoTrail_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		//: each P formats from the shared immutable sentinel.
		for pb.Next() {
			sinkErrString = benchErrSentinel.Error()
		}
	})
}

// BenchmarkError_Error_Trail measures the formatter on the trail path, where a
// strings.Builder renders the wrap-site codes between origin and Reason. The
// number quantifies the Builder cost over the no-trail fast path.
func BenchmarkError_Error_Trail(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: trail-present path — strings.Builder assembles the bracket header.
		sinkErrString = benchWrappedTrail.Error()
	}
}

// BenchmarkError_Source measures the wrapped-cause getter — a direct field
// read, 0 allocs/op.
func BenchmarkError_Source(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: direct read of the wrapped cause.
		sinkErrError = benchWrappedTrail.Source()
	}
}

// BenchmarkError_Unwrap measures the Unwrap delegate (nil-guard + Source read),
// the entry point stdlib errors.Is/As use — 0 allocs/op.
func BenchmarkError_Unwrap(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: nil-guarded delegate to Source — what stdlib walking calls.
		sinkErrError = benchWrappedTrail.Unwrap()
	}
}

// BenchmarkHasCode measures the chain-walking Code matcher on an origin hit at
// the deepest layer. The walk recurses Unwrap without allocating — 0 allocs/op.
func BenchmarkHasCode(b *testing.B) {
	chain := fmt.Errorf("outer: %w", benchErrSentinel)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: walk single- and multi-Unwrap legs searching for the origin Code.
		sinkErrBool = HasCode(chain, Pack(3, 2, 4, 1))
	}
}

// BenchmarkHasCode_Parallel confirms HasCode walks are contention-free.
func BenchmarkHasCode_Parallel(b *testing.B) {
	chain := fmt.Errorf("outer: %w", benchErrSentinel)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		//: each P walks the shared chain searching for the Code independently.
		for pb.Next() {
			sinkErrBool = HasCode(chain, Pack(3, 2, 4, 1))
		}
	})
}

// BenchmarkHasCode_Trail measures HasCode when the match lands in the trail
// rather than the origin code, exercising the slices.Contains trail scan.
func BenchmarkHasCode_Trail(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: trail-hit path — the matched Code lives in the wrap trail.
		sinkErrBool = HasCode(benchWrappedTrail, Pack(3, 2, 4, 2))
	}
}

// BenchmarkError_Is_Prefix measures the PrefixMatcher branch of (*Error).Is —
// the CIDR-style origin+trail scan (path 1 of the three Is modes).
func BenchmarkError_Is_Prefix(b *testing.B) {
	pm := NewPrefixMatcher(Pack(3, 0, 0, 0), MaskByMajor)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: prefix path — match origin code or any trail entry against the mask.
		sinkErrBool = benchWrappedTrail.Is(pm)
	}
}

// BenchmarkError_Is_Sentinel measures the sentinel-by-Code branch of
// (*Error).Is — two *Error instances matched on (Code, Reason) (path 2).
func BenchmarkError_Is_Sentinel(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: sentinel path — Code+Reason equivalence across pointer identities.
		sinkErrBool = benchWrappedTrail.Is(benchErrSentinel)
	}
}

// BenchmarkError_Is_Pointer measures the default pointer-equality branch of
// (*Error).Is, taken when the target is neither a PrefixMatcher nor an *Error
// (path 3).
func BenchmarkError_Is_Pointer(b *testing.B) {
	target := errors.New("unrelated target")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: default path — fall through to stdlib pointer equality.
		sinkErrBool = benchErrSentinel.Is(target)
	}
}
