package errs

import (
	"testing"
)

// benchMatcher is a representative /8 matcher (all codes sharing Major). Built
// once so the accessor benches isolate the getter, not construction.
var benchMatcher = NewPrefixMatcher(Pack(2, 0, 0, 0), MaskByMajor)

// Result sinks for the PrefixMatcher surface.
var (
	sinkMatcher       *PrefixMatcher
	sinkMatcherCode   Code
	sinkMatcherString string
)

// BenchmarkNewPrefixMatcher measures the matcher constructor. It returns a
// pointer, so the struct escapes to the heap — bounded at 1 alloc/op, the
// matcher-build floor.
func BenchmarkNewPrefixMatcher(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: heap-allocate the matcher behind a pointer each iteration.
		sinkMatcher = NewPrefixMatcher(Pack(2, 0, 0, 0), MaskByMajor)
	}
}

// BenchmarkPrefixMatcher_Prefix measures the prefix getter — a direct field
// read, must be 0 allocs/op.
func BenchmarkPrefixMatcher_Prefix(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: direct read of the immutable prefix.
		sinkMatcherCode = benchMatcher.Prefix()
	}
}

// BenchmarkPrefixMatcher_Prefix_Parallel confirms the prefix read is
// contention-free.
func BenchmarkPrefixMatcher_Prefix_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		//: immutable field — safe to read across all P goroutines.
		for pb.Next() {
			sinkMatcherCode = benchMatcher.Prefix()
		}
	})
}

// BenchmarkPrefixMatcher_Mask measures the mask getter — a direct field read,
// must be 0 allocs/op.
func BenchmarkPrefixMatcher_Mask(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: direct read of the immutable mask.
		sinkMatcherCode = benchMatcher.Mask()
	}
}

// BenchmarkPrefixMatcher_Mask_Parallel confirms the mask read is
// contention-free.
func BenchmarkPrefixMatcher_Mask_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		//: immutable field read from every P.
		for pb.Next() {
			sinkMatcherCode = benchMatcher.Mask()
		}
	})
}

// BenchmarkPrefixMatcher_String measures the diagnostic formatter, which calls
// Code.String() twice and concatenates — it allocates the result string.
func BenchmarkPrefixMatcher_String(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: format "PrefixMatcher{prefix=…, mask=…}" — allocates the string.
		sinkMatcherString = benchMatcher.String()
	}
}

// BenchmarkPrefixMatcher_String_Parallel confirms the formatter reads only
// immutable state and is contention-free.
func BenchmarkPrefixMatcher_String_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		//: each P formats independently from the shared immutable matcher.
		for pb.Next() {
			sinkMatcherString = benchMatcher.String()
		}
	})
}

// BenchmarkPrefixMatcher_Error measures the error-interface conformance, which
// is identical to String() — benched separately to gate that the errors.Is
// protocol concession costs no more than the diagnostic formatter.
func BenchmarkPrefixMatcher_Error(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: Error() delegates to String() — same allocation shape.
		sinkMatcherString = benchMatcher.Error()
	}
}
