package metrics_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/metrics"
)

// : sinks so the compiler cannot prove the constructed values dead. Every
// : benchmark here measures a function whose result is otherwise discardable.
var (
	attrSink  metrics.AttrValue
	bytesSink []byte
	intSink   int
	sliceSink []metrics.AttrValue
)

// : one shared identity buffer, reset per iteration. A fresh make() inside the
// : loop would measure the allocator rather than AppendIdentity.
var identityBuf = make([]byte, 0, 256)

// BenchmarkString / Bool / Int64 / Float64 pin the four constructors at zero
// allocations. ADR 0044 makes the attribute KIND part of a series' identity,
// which is only affordable if writing one costs nothing — these are the floor
// every attributed observation is built on.
func BenchmarkString(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		attrSink = metrics.String("http.route", "/v1/orders/{id}")
	}
}

func BenchmarkBool(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		attrSink = metrics.Bool("cache.hit", true)
	}
}

func BenchmarkInt64(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		attrSink = metrics.Int64("http.status_code", 503)
	}
}

func BenchmarkFloat64(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		attrSink = metrics.Float64("sampling.ratio", 0.125)
	}
}

// BenchmarkAppendIdentity_String is the hot path of series lookup: a meter
// builds the identity of an attribute set on every attributed observation, and
// ADR 0044's zero-allocation claim rests on this reusing the caller's buffer.
// A non-zero alloc count here is the regression.
func BenchmarkAppendIdentity_String(b *testing.B) {
	a := metrics.String("http.route", "/v1/orders/{id}")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink = a.AppendIdentity(identityBuf[:0])
	}
}

// BenchmarkAppendIdentity_Int64 is the contrast: a fixed-width kind writes no
// length prefix and no variable bytes, so it is the cheapest identity there is.
func BenchmarkAppendIdentity_Int64(b *testing.B) {
	a := metrics.Int64("http.status_code", 503)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink = a.AppendIdentity(identityBuf[:0])
	}
}

// BenchmarkAppendIdentity_Set4 is what a realistic observation actually pays:
// four attributes appended into one buffer, which is the identity a meter keys
// its series map on.
func BenchmarkAppendIdentity_Set4(b *testing.B) {
	set := []metrics.AttrValue{
		metrics.String("http.route", "/v1/orders/{id}"),
		metrics.String("http.method", "GET"),
		metrics.Int64("http.status_code", 200),
		metrics.Bool("cache.hit", true),
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		dst := identityBuf[:0]
		for _, a := range set {
			dst = a.AppendIdentity(dst)
		}
		bytesSink = dst
	}
}

// BenchmarkAppendText is the exporter-side rendering path — human-readable
// rather than injective — and exists so the two are never confused for one
// another in a profile.
func BenchmarkAppendText(b *testing.B) {
	a := metrics.String("http.route", "/v1/orders/{id}")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink = a.AppendText(identityBuf[:0])
	}
}

// BenchmarkCompareAttrValue_SameKind and _DifferentKind price the total order a
// Collect-time sort walks. The different-kind case short-circuits on the tag,
// so it is the cheap one, and the delta says how much of a sort is payload
// comparison rather than dispatch.
func BenchmarkCompareAttrValue_SameKind(b *testing.B) {
	x := metrics.String("k", "/v1/orders/{id}")
	y := metrics.String("k", "/v1/orders/{id}/items")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		intSink = metrics.CompareAttrValue(x, y)
	}
}

func BenchmarkCompareAttrValue_DifferentKind(b *testing.B) {
	x := metrics.String("k", "v")
	y := metrics.Int64("k", 1)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		intSink = metrics.CompareAttrValue(x, y)
	}
}

// BenchmarkValidateAttrs_Set4 measures the check every instrument fetch runs:
// empty key, unset kind, duplicate key. It walks a sorted set once and must
// not allocate — a validation that allocates on the hot path would undo the
// design AppendIdentity exists to protect.
func BenchmarkValidateAttrs_Set4(b *testing.B) {
	sorted := metrics.SortAttrs([]metrics.AttrValue{
		metrics.Bool("cache.hit", true),
		metrics.String("http.method", "GET"),
		metrics.String("http.route", "/v1/orders/{id}"),
		metrics.Int64("http.status_code", 200),
	})
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		metrics.ValidateAttrs(sorted)
	}
}

// BenchmarkSortAttrs_Set4 is the deliberately COLD path: it copies, sorts and
// validates, and its doc comment says Resource and Scope call it once at
// construction and never per observation. The allocation here is why — this
// number is the argument for the stack-buffer path the meter uses instead.
func BenchmarkSortAttrs_Set4(b *testing.B) {
	attrs := []metrics.AttrValue{
		metrics.String("http.route", "/v1/orders/{id}"),
		metrics.Bool("cache.hit", true),
		metrics.Int64("http.status_code", 200),
		metrics.String("http.method", "GET"),
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sliceSink = metrics.SortAttrs(attrs)
	}
}
