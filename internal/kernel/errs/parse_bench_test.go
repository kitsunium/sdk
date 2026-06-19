package errs

import (
	"testing"
)

// Result sinks for ParseCode. The parser is single-pass and allocation-free on
// the success path (it returns a packed Code value); the failure path
// allocates the *Error diagnostic.
var (
	sinkParseCode Code
	sinkParseErr  error
)

// BenchmarkParseCode measures the strict canonical parser on a valid
// "M.L.P.S" input. The success path is single-pass with no allocations — it
// returns a packed Code value, so it must be 0 allocs/op.
func BenchmarkParseCode(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: parse a well-formed canonical code — the allocation-free happy path.
		sinkParseCode, sinkParseErr = ParseCode("12.34.56.78")
	}
}

// BenchmarkParseCode_Parallel confirms ParseCode is goroutine-safe — it reads
// only its string argument and shares no state — and stays alloc-free under
// contention.
func BenchmarkParseCode_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		//: each P parses an independent copy of the literal — no shared state.
		for pb.Next() {
			sinkParseCode, sinkParseErr = ParseCode("12.34.56.78")
		}
	})
}

// BenchmarkParseCode_Invalid measures the failure path (out-of-range segment),
// where parseFailure allocates the diagnostic *Error. The number quantifies
// the cost of a rejected input versus the success path.
func BenchmarkParseCode_Invalid(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: "256" overflows the octet bound — exercises the *Error failure path.
		sinkParseCode, sinkParseErr = ParseCode("256.0.0.1")
	}
}
