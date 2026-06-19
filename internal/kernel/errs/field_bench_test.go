package errs

import (
	"testing"
)

// Result sinks for the FieldValue constructors and accessors. FieldValue is a
// value type, so its constructors return a struct by value — the benches gate
// the 0-alloc struct-literal contract.
var (
	sinkField       FieldValue
	sinkFieldString string
)

// BenchmarkFieldString measures the string-typed constructor — a struct
// literal only, must be 0 allocs/op.
func BenchmarkFieldString(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: build a string FieldValue by value — no heap escape.
		sinkField = String("key", "value")
	}
}

// BenchmarkFieldInt measures the int constructor (widens to int64 internally) —
// struct literal, 0 allocs/op.
func BenchmarkFieldInt(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: int widened to int64 then stored — pure value construction.
		sinkField = Int("key", 42)
	}
}

// BenchmarkFieldInt64 measures the int64 constructor (no upcast) — struct
// literal, 0 allocs/op.
func BenchmarkFieldInt64(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: int64 stored verbatim — no widening, no allocation.
		sinkField = Int64("key", 42)
	}
}

// BenchmarkFieldBool measures the bool constructor — struct literal, 0
// allocs/op.
func BenchmarkFieldBool(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: direct bool assignment into the value struct.
		sinkField = Bool("key", true)
	}
}

// BenchmarkFieldFloat measures the float64 constructor — struct literal, 0
// allocs/op.
func BenchmarkFieldFloat(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: 64-bit float stored by value.
		sinkField = Float("key", 3.14)
	}
}

// BenchmarkNewFieldValue measures the New-prefixed generic constructor; it
// forwards to String, so it must track BenchmarkFieldString — 0 allocs/op.
func BenchmarkNewFieldValue(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: thin forward to String — benched to gate the alias cost.
		sinkField = NewFieldValue("key", "value")
	}
}

// BenchmarkFieldValue_Key measures the immutable Key getter — a field read, 0
// allocs/op.
func BenchmarkFieldValue_Key(b *testing.B) {
	f := String("key", "value")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: direct read of the immutable key.
		sinkFieldString = f.Key()
	}
}

// BenchmarkFieldValue_StringValue_String measures the textual restitution for a
// string payload — returns the stored string verbatim, 0 allocs/op.
func BenchmarkFieldValue_StringValue_String(b *testing.B) {
	f := String("key", "value")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: string kind returns the stored value directly — no formatting.
		sinkFieldString = f.StringValue()
	}
}

// BenchmarkFieldValue_StringValue_Int measures the int payload rendering, which
// runs strconv.FormatInt and therefore allocates the formatted string — the
// number quantifies the non-string restitution cost.
func BenchmarkFieldValue_StringValue_Int(b *testing.B) {
	f := Int("key", 1234567)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: int kind formats base-10 via strconv — allocates the result string.
		sinkFieldString = f.StringValue()
	}
}

// BenchmarkFieldValue_StringValue_Float measures the float payload rendering
// (strconv.FormatFloat shortest round-trip), the most expensive restitution
// path.
func BenchmarkFieldValue_StringValue_Float(b *testing.B) {
	f := Float("key", 3.14159)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: float kind formats shortest round-trip — the costliest restitution.
		sinkFieldString = f.StringValue()
	}
}
