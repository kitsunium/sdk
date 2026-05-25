// Benchmarks for the errs package — sized to the operations every
// consumer hits on a hot path: build, wrap, render, classify.
//
// Methodology disclosed in pkg/v1/errs/BENCH.md alongside the numbers.
// Stdlib baselines (errors.New + fmt.Errorf) are present so the typed-
// error overhead is visible — not hidden behind absolute numbers.
//
// Run: `cd pkg/v1/errs && go test -bench=. -benchmem -run='^$'`
package errs_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// Package-level sinks defeat the dead-store optimisation: assignments
// to a `_` are eligible for elimination at -O2, which would skew bench
// numbers downward. The compiler keeps these writes alive because the
// vars are observable from a sibling test (the linter is also happy —
// no "dead assignment" complaints).
var (
	errBenchSentinel = errors.New("benchmark sentinel cause")
	errBenchMatcher  = errs.NewPrefixMatcher(errs.Pack(1, 1, 1, 1), errs.MaskByMajor)

	benchErrSink  error     //nolint:errcheck // bench sink
	benchCodeSink errs.Code //nolint:unused   // bench sink
	benchBoolSink bool      //nolint:unused   // bench sink
	benchStrSink  string    //nolint:unused   // bench sink
)

// BenchmarkErrorsNewBaseline measures stdlib errors.New — the simplest
// way to build an error in Go. The typed-error API must stay within a
// small multiple of this floor.
func BenchmarkErrorsNewBaseline(b *testing.B) {
	for b.Loop() {
		benchErrSink = errors.New("baseline")
	}
}

// BenchmarkFmtErrorfBaseline measures stdlib fmt.Errorf — the typical
// "wrap + format" path in idiomatic Go.
func BenchmarkFmtErrorfBaseline(b *testing.B) {
	for b.Loop() {
		benchErrSink = fmt.Errorf("baseline %w", errBenchSentinel)
	}
}

// BenchmarkFmtErrorfRender measures the cost of rendering a stdlib-
// wrapped error's message — the comparison point for our Error().
func BenchmarkFmtErrorfRender(b *testing.B) {
	wrapped := fmt.Errorf("baseline %w", errBenchSentinel)
	b.ResetTimer()
	for b.Loop() {
		benchStrSink = wrapped.Error()
	}
}

// BenchmarkCodeOf measures the per-call cost of read-only introspection
// on a typed error — the hot path for routers / sinks deciding what to
// do with an error. CodeOf returns (Code, bool); we capture both.
func BenchmarkCodeOf(b *testing.B) {
	wrapped := fmt.Errorf("baseline %w", errBenchSentinel)
	b.ResetTimer()
	for b.Loop() {
		benchCodeSink, benchBoolSink = errs.CodeOf(wrapped)
	}
}

// BenchmarkHasCode_NoMatch measures errs.HasCode traversal — walks the
// Unwrap chain looking for a Code match. Worst case here is the
// no-match path (the stdlib wrap carries no Code).
func BenchmarkHasCode_NoMatch(b *testing.B) {
	wrapped := fmt.Errorf("baseline %w", errBenchSentinel)
	target := errs.Pack(99, 99, 99, 99)
	b.ResetTimer()
	for b.Loop() {
		benchBoolSink = errs.HasCode(wrapped, target)
	}
}

// BenchmarkPrefixMatcher exercises the PrefixMatcher subnet match via
// errors.Is — reference cost for code-range classification.
func BenchmarkPrefixMatcher(b *testing.B) {
	wrapped := fmt.Errorf("baseline %w", errBenchSentinel)
	b.ResetTimer()
	for b.Loop() {
		benchBoolSink = errors.Is(wrapped, errBenchMatcher)
	}
}
