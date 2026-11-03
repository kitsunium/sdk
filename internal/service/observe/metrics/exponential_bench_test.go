// Package metrics — benchmarks for the exponential histogram and the
// exemplar paths (#869), hoisted instruments: the cost measured is the
// record, not the lookup.
package metrics

import (
	"context"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
)

// benchSampled is a context carrying a sampled span.
var benchSampled = coretrace.ContextWithSpanContext(context.Background(), coretrace.SpanContextValue{
	TraceID: coretrace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
	SpanID:  coretrace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
	Flags:   coretrace.TraceFlags(1),
})

// benchLatencies is a spread of latencies in seconds, 10 µs to 3 s.
var benchLatencies = [8]float64{1e-5, 3e-4, 2e-3, 0.012, 0.045, 0.2, 0.9, 3}

// BenchmarkExponentialRecord records into an exponential histogram of the
// default size once the scale has settled — the steady state.
func BenchmarkExponentialRecord(b *testing.B) {
	h := newMemMeter(MeterConfig{}).ExponentialHistogram("latency", 0)
	for _, v := range benchLatencies {
		h.Record(v)
	}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		h.Record(benchLatencies[i&7])
		i++
	}
}

// BenchmarkHistogramRecordContext_Sampled is an explicit histogram's record
// that keeps its bucket's exemplar: the span read, the clock read, four
// stores under the reservoir's lock.
func BenchmarkHistogramRecordContext_Sampled(b *testing.B) {
	h := newMemMeter(MeterConfig{}).Histogram("latency", coremetrics.DefaultBuckets()).(coremetrics.ContextHistogram)
	h.RecordContext(benchSampled, 1)
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		h.RecordContext(benchSampled, benchLatencies[i&7]*1000)
		i++
	}
}

// BenchmarkCounterAddContext_Unsampled is a counter's AddContext under a
// context with no span: the add and one context lookup, no exemplar.
func BenchmarkCounterAddContext_Unsampled(b *testing.B) {
	c := newMemMeter(MeterConfig{}).Counter("requests").(coremetrics.ContextCounter)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		c.AddContext(ctx, 1)
	}
}
