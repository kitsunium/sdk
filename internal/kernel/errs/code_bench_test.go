package errs

import (
	"testing"
)

// Representative dotted-quad Code used across the Code-method benches. A
// fixed, non-zero value keeps every iteration on the same shift/mask path so
// the numbers isolate the operation under test, not input variance.
var benchCode = Pack(1, 2, 3, 4)

// Package-level sinks keep the optimiser from eliding the benched calls. Each
// type has its own sink so the assignment never needs a runtime conversion.
var (
	sinkCode    Code
	sinkMajor   Major
	sinkLayer   Layer
	sinkPkgCode PkgCode
	sinkSerial  Serial
	sinkString  string
)

// BenchmarkPack measures the runtime Code constructor — four shifts ORed
// together. Pure uint32 math, so it must report 0 allocs/op; it is the floor
// every other Code operation is read against.
func BenchmarkPack(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: rebuild the Code each iteration so the shift/OR work is benched.
		sinkCode = Pack(1, 2, 3, 4)
	}
}

// BenchmarkPack_Parallel measures Pack under RunParallel to confirm the
// constructor is goroutine-safe (it touches no shared state) and stays
// alloc-free under contention.
func BenchmarkPack_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		//: each P independently rebuilds the Code — no shared state to contend.
		for pb.Next() {
			sinkCode = Pack(1, 2, 3, 4)
		}
	})
}

// BenchmarkCode_Major measures the top-octet accessor — a single unsigned
// shift. Must be 0 allocs/op.
func BenchmarkCode_Major(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: shift-and-truncate read; no allocation possible.
		sinkMajor = benchCode.Major()
	}
}

// BenchmarkCode_Layer measures the second-octet accessor — same shift shape
// as Major. Must be 0 allocs/op.
func BenchmarkCode_Layer(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: second-octet shift; benched alongside Major for symmetry.
		sinkLayer = benchCode.Layer()
	}
}

// BenchmarkCode_Package measures the third-octet accessor. Must be 0 allocs/op.
func BenchmarkCode_Package(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: third-octet shift-and-truncate.
		sinkPkgCode = benchCode.Package()
	}
}

// BenchmarkCode_Serial measures the bottom-octet accessor — a plain uint8
// cast, no shift. Must be 0 allocs/op.
func BenchmarkCode_Serial(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: low-byte cast keeps only the bottom octet.
		sinkSerial = benchCode.Serial()
	}
}

// BenchmarkCode_String measures the canonical "M.L.P.S" formatter. It calls
// strconv.Itoa four times and concatenates, so it allocates the result
// string — the number quantifies the cost consumers pay when logging a Code.
func BenchmarkCode_String(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: format the canonical unpadded dotted form each iteration.
		sinkString = benchCode.String()
	}
}

// BenchmarkCode_String_Parallel measures the formatter under RunParallel; it
// reads only the immutable Code, so it must stay correct and contention-free.
func BenchmarkCode_String_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		//: each P formats independently — the Code value is read-only.
		for pb.Next() {
			sinkString = benchCode.String()
		}
	})
}

// BenchmarkCode_Padded measures the zero-padded "MMM.LLL.PPP.SSS" display
// formatter — same allocation shape as String but with the padding switch.
func BenchmarkCode_Padded(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: format the padded display form each iteration.
		sinkString = benchCode.Padded()
	}
}

// BenchmarkCode_Padded_Parallel measures Padded under RunParallel for the same
// read-only goroutine-safety guarantee as the String variant.
func BenchmarkCode_Padded_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		//: each P formats the padded form from the shared immutable Code.
		for pb.Next() {
			sinkString = benchCode.Padded()
		}
	})
}
