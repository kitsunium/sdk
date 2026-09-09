// Package metrics — benchmarks for the labelled series hot path.
//
// What is being measured is the LOOKUP, not the arithmetic. Before labels a
// caller could hoist `m.Counter("x")` out of the loop once and never look it up
// again; with labels the values are per-observation (`status="503"`), so the
// lookup moves onto the request path and its cost and its allocations become
// the feature's real budget.
package metrics

import (
	"strconv"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
)

// benchLabels is a realistic three-dimension label set, deliberately given out
// of key order so the sort is inside the measurement.
var benchLabels = []coremetrics.LabelValue{
	{Key: "status", Value: "200"},
	{Key: "method", Value: "GET"},
	{Key: "route", Value: "/v1/widgets"},
}

// BenchmarkCounterLookup_NoLabels is the pre-label call shape, unchanged.
func BenchmarkCounterLookup_NoLabels(b *testing.B) {
	m := NewMeter()
	m.Counter("http_requests_total").Inc()
	b.ReportAllocs()
	for b.Loop() {
		m.Counter("http_requests_total").Inc()
	}
}

// BenchmarkCounterLookup_3Labels is the labelled call shape: sort, encode,
// resolve, increment.
func BenchmarkCounterLookup_3Labels(b *testing.B) {
	m := NewMeter()
	m.Counter("http_requests_total", benchLabels...).Inc()
	b.ReportAllocs()
	for b.Loop() {
		m.Counter("http_requests_total", benchLabels...).Inc()
	}
}

// BenchmarkCounterLookup_Overflow measures the path a service takes once its
// labels have blown up: every observation carries a label set never seen
// before, so every one of them misses the read lock and folds into overflow.
func BenchmarkCounterLookup_Overflow(b *testing.B) {
	m := NewMeterWithConfig(MeterConfig{MaxSeriesPerInstrument: 1})
	m.Counter("http_requests_total").Inc()
	i := 0
	b.ReportAllocs()
	for b.Loop() {
		i++
		m.Counter("http_requests_total", coremetrics.LabelValue{
			Key: "request_id", Value: strconv.Itoa(i),
		}).Inc()
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
	m.Counter("http_requests_total", benchLabels...).Inc()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			m.Counter("http_requests_total", benchLabels...).Inc()
		}
	})
}

// BenchmarkCollect_1000Names is the scrape path in the PRE-LABEL shape — a
// thousand dimensionless instruments — so it compares directly with the
// baseline this change was measured against.
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

// BenchmarkCollect_1000Series is the scrape path in the LABELLED shape — one
// instrument carrying a thousand series, which is what a real Prometheus
// exporter will be handed.
func BenchmarkCollect_1000Series(b *testing.B) {
	m := NewMeter()
	for i := range 1000 {
		m.Counter("http_requests_total", coremetrics.LabelValue{
			Key: "route", Value: strconv.Itoa(i),
		}).Inc()
	}
	b.ReportAllocs()
	for b.Loop() {
		m.Collect()
	}
}

// BenchmarkHistogramRecord is the instrument arithmetic, unchanged by labels.
func BenchmarkHistogramRecord(b *testing.B) {
	m := NewMeter()
	h := m.Histogram("latency", []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10})
	b.ReportAllocs()
	for b.Loop() {
		h.Record(0.3)
	}
}
