// Package metrics — benchmarks for the attributed series hot path.
//
// What is being measured is the LOOKUP, not the arithmetic. Without attributes
// a caller could hoist `m.Counter("x")` out of the loop once and never look it
// up again; with them the values are per-observation
// (`http.response.status_code=503`), so the lookup moves onto the request path
// and its cost and its allocations become the feature's real budget.
package metrics

import (
	"strconv"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
)

// benchAttrs is a realistic three-dimension attribute set, deliberately given
// out of key order so the sort is inside the measurement.
var benchAttrs = []coremetrics.AttrValue{
	coremetrics.String("status", "200"),
	coremetrics.String("method", "GET"),
	coremetrics.String("route", "/v1/widgets"),
}

// BenchmarkCounterLookup_NoAttrs is the dimensionless call shape, unchanged
// since before attributes existed.
func BenchmarkCounterLookup_NoAttrs(b *testing.B) {
	m := NewMeter()
	m.Counter("http_requests_total").Inc()
	b.ReportAllocs()
	for b.Loop() {
		m.Counter("http_requests_total").Inc()
	}
}

// BenchmarkCounterLookup_3Attrs is the attributed call shape: sort, encode,
// resolve, increment.
func BenchmarkCounterLookup_3Attrs(b *testing.B) {
	m := NewMeter()
	m.Counter("http_requests_total", benchAttrs...).Inc()
	b.ReportAllocs()
	for b.Loop() {
		m.Counter("http_requests_total", benchAttrs...).Inc()
	}
}

// BenchmarkCounterLookup_Overflow measures the path a service takes once its
// attributes have blown up: every observation carries a set never seen before,
// so every one of them misses the read lock and folds into overflow.
func BenchmarkCounterLookup_Overflow(b *testing.B) {
	m := NewMeterWithConfig(MeterConfig{MaxSeriesPerInstrument: 1})
	m.Counter("http_requests_total").Inc()
	i := 0
	b.ReportAllocs()
	for b.Loop() {
		i++
		m.Counter("http_requests_total", coremetrics.String("request_id", strconv.Itoa(i))).Inc()
	}
}

// BenchmarkCounterAdd_Hoisted is the arithmetic alone, for the caller who can
// still hoist the instrument.
func BenchmarkCounterAdd_Hoisted(b *testing.B) {
	m := NewMeter()
	c := m.Counter("http_requests_total")
	b.ReportAllocs()
	for b.Loop() {
		c.Inc()
	}
}

// BenchmarkCounterLookup_Parallel is the lookup under contention — the reason
// an existing series resolves under the READ lock.
func BenchmarkCounterLookup_Parallel(b *testing.B) {
	m := NewMeter()
	m.Counter("http_requests_total", benchAttrs...).Inc()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			m.Counter("http_requests_total", benchAttrs...).Inc()
		}
	})
}

// BenchmarkCollect_1000Names is the scrape path over a thousand DIMENSIONLESS
// instruments, so it compares directly with every earlier baseline.
func BenchmarkCollect_1000Names(b *testing.B) {
	m := NewMeter()
	for i := range 1000 {
		m.Counter("http_requests_total_" + strconv.Itoa(i)).Inc()
	}
	b.ReportAllocs()
	for b.Loop() {
		m.Collect()
	}
}

// BenchmarkCollect_1000Series is the scrape path over ONE instrument carrying a
// thousand series, which is what a real Prometheus exporter will be handed.
func BenchmarkCollect_1000Series(b *testing.B) {
	m := NewMeter()
	for i := range 1000 {
		m.Counter("http_requests_total", coremetrics.String("route", strconv.Itoa(i))).Inc()
	}
	b.ReportAllocs()
	for b.Loop() {
		m.Collect()
	}
}

// BenchmarkCounterLookup_3Attrs_Described is BenchmarkCounterLookup_3Attrs on a
// meter that HAS a description map. It exists to make the ADR 0067 claim
// falsifiable: a description is wiring-time state that the fetch path never
// reads, so these two numbers must be the same line.
func BenchmarkCounterLookup_3Attrs_Described(b *testing.B) {
	m := NewMeter()
	m.(coremetrics.Describer).Describe("http_requests_total", "Requests served, by route and status")
	m.Counter("http_requests_total", benchAttrs...).Inc()
	b.ReportAllocs()
	for b.Loop() {
		m.Counter("http_requests_total", benchAttrs...).Inc()
	}
}

// BenchmarkCollect_1000Names_Described is where the description IS read: once
// per instrument name, on the scrape path, out of a map with a thousand entries
// in it. Against BenchmarkCollect_1000Names it prices the whole feature.
func BenchmarkCollect_1000Names_Described(b *testing.B) {
	m := NewMeter()
	describer := m.(coremetrics.Describer)
	for i := range 1000 {
		name := "http_requests_total_" + strconv.Itoa(i)
		describer.Describe(name, "Requests served by handler "+strconv.Itoa(i))
		m.Counter(name).Inc()
	}
	b.ReportAllocs()
	for b.Loop() {
		m.Collect()
	}
}

// BenchmarkDescribe is the wiring-time call itself, on its idempotent path — the
// shape a caller who describes from two packages actually takes. It is here for
// completeness rather than because it is on any hot path: it runs once per
// instrument name at start-up.
func BenchmarkDescribe(b *testing.B) {
	describer := NewMeter().(coremetrics.Describer)
	describer.Describe("http_requests_total", "Requests served")
	b.ReportAllocs()
	for b.Loop() {
		describer.Describe("http_requests_total", "Requests served")
	}
}

// BenchmarkHistogramRecord is the instrument arithmetic, unchanged by attributes.
func BenchmarkHistogramRecord(b *testing.B) {
	m := NewMeter()
	h := m.Histogram("latency", []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10})
	b.ReportAllocs()
	for b.Loop() {
		h.Record(0.3)
	}
}

// benchTypedAttrs is the same three dimensions with the kinds an OTel-shaped
// call site actually writes: the status code is an integer, not the string
// "200". The comparison against benchAttrs is what says whether typing the
// attribute model cost anything on the observation path.
var benchTypedAttrs = []coremetrics.AttrValue{
	coremetrics.Int64("status", 200),
	coremetrics.String("method", "GET"),
	coremetrics.Bool("cached", false),
}

// BenchmarkCounterLookup_3TypedAttrs resolves a three-attribute series whose
// values are NOT all strings — the fixed-width encoding path in the series key.
func BenchmarkCounterLookup_3TypedAttrs(b *testing.B) {
	m := NewMeter()
	m.Counter("http_requests_total", benchTypedAttrs...).Inc()
	b.ReportAllocs()
	for b.Loop() {
		m.Counter("http_requests_total", benchTypedAttrs...).Inc()
	}
}

// BenchmarkUpDownCounterLookup_3Attrs resolves a non-monotonic sum, which lives
// in the SAME store as a counter and is told apart by the instrument-kind byte
// that opens the series key.
func BenchmarkUpDownCounterLookup_3Attrs(b *testing.B) {
	m := NewMeter()
	m.UpDownCounter("in_flight", benchAttrs...).Inc()
	b.ReportAllocs()
	for b.Loop() {
		m.UpDownCounter("in_flight", benchAttrs...).Dec()
	}
}

// BenchmarkCollect_1000Series_Delta is the scrape path for a delta reader,
// where Collect is a MUTATION: every accumulator is swapped to zero rather than
// read. It is the cost of the temporality the model made explicit.
func BenchmarkCollect_1000Series_Delta(b *testing.B) {
	m := NewMeterWithConfig(MeterConfig{Temporality: coremetrics.TemporalityDelta})
	for i := range 1000 {
		m.Counter("http_requests_total", coremetrics.String("route", strconv.Itoa(i))).Inc()
	}
	b.ReportAllocs()
	for b.Loop() {
		m.Collect()
	}
}

// BenchmarkCollect_100Observables is the scrape path with asynchronous
// instruments, where every callback is run inside the collection. It measures
// what an observable costs a scrape that a synchronous instrument does not.
func BenchmarkCollect_100Observables(b *testing.B) {
	m := NewMeter()
	for i := range 100 {
		m.ObservableCounter("observed_"+strconv.Itoa(i), constantObserver(int64(i)))
	}
	b.ReportAllocs()
	for b.Loop() {
		m.Collect()
	}
}

// constantObserver returns a callback reporting value, built OUTSIDE the loop
// that registers it so the value is a parameter rather than a captured
// variable — the benchmark measures the collection, not a closure escape.
func constantObserver(value int64) coremetrics.Int64Callback {
	//: one callback per registration, each closing over its own argument.
	return func(observe coremetrics.ObserveInt64) {
		//: report the fixed absolute total.
		observe(value)
	}
}
