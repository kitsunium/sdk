package metrics_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svcmetrics "github.com/kitsunium/sdk/internal/service/observe/metrics"
)

// The span every exemplar test records under, and its hex spelling.
var (
	exTraceID = coretrace.TraceID{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6, 0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36}
	exSpanID  = coretrace.SpanID{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7}
	exAt      = time.Date(2026, 1, 2, 3, 4, 10, 0, time.UTC)
)

// tracedCtx carries the test span, sampled or not.
func tracedCtx(sampled bool) context.Context {
	flags := coretrace.TraceFlags(0)
	if sampled {
		flags = coretrace.TraceFlags(1)
	}
	return coretrace.ContextWithSpanContext(context.Background(), coretrace.SpanContextValue{TraceID: exTraceID, SpanID: exSpanID, Flags: flags})
}

// exemplarMeter is a cumulative meter on a manual clock standing at exAt.
func exemplarMeter(temporality coremetrics.Temporality) coremetrics.FullMeter {
	return svcmetrics.NewMeterWithConfig(svcmetrics.MeterConfig{Clock: clock.NewManualClock(exAt), Temporality: temporality})
}

// TestASampledSpanLeavesAnExemplarOnTheSum (#869): AddContext under a
// sampled span keeps the measurement — value, time, trace and span — as the
// sum point's exemplar; under an unsampled span, or none, it keeps nothing
// and still adds; a later sampled measurement replaces it.
func TestASampledSpanLeavesAnExemplarOnTheSum(t *testing.T) {
	t.Parallel()
	m := exemplarMeter(coremetrics.TemporalityCumulative)
	counter, ok := m.Counter("requests").(coremetrics.ContextCounter)
	if !ok {
		t.Fatal("a Counter is not a ContextCounter")
	}
	counter.AddContext(tracedCtx(false), 3)
	counter.AddContext(context.Background(), 4)
	if snap := m.Collect(); snap.Sums["requests"].Points[0].Exemplars != nil || snap.Sums["requests"].Points[0].Value != 7 {
		t.Fatalf("an unsampled measurement left %+v", snap.Sums["requests"].Points[0])
	}
	counter.AddContext(tracedCtx(true), 5)
	counter.AddContext(tracedCtx(true), 6)
	point := m.Collect().Sums["requests"].Points[0]
	want := coremetrics.ExemplarValue{Time: exAt, Value: 6, TraceID: exTraceID, SpanID: exSpanID}
	if point.Value != 18 || len(point.Exemplars) != 1 || point.Exemplars[0] != want {
		t.Fatalf("point = %+v, want 18 with the last sampled exemplar %+v", point, want)
	}
	if again := m.Collect().Sums["requests"].Points[0].Exemplars; len(again) != 1 {
		t.Fatalf("a cumulative meter dropped its exemplar at the next collection: %+v", again)
	}
}

// TestAnExplicitHistogramKeepsOneExemplarPerBucket: the exemplar of each
// bucket is the last sampled measurement that fell in it, in bucket order,
// and a delta collection hands them over once.
func TestAnExplicitHistogramKeepsOneExemplarPerBucket(t *testing.T) {
	t.Parallel()
	m := exemplarMeter(coremetrics.TemporalityDelta)
	hist := m.Histogram("latency", []float64{1, 10}).(coremetrics.ContextHistogram)
	hist.RecordContext(tracedCtx(true), 20)
	hist.RecordContext(tracedCtx(true), 0.5)
	hist.RecordContext(tracedCtx(true), 0.7)
	hist.RecordContext(tracedCtx(false), 5)
	point := m.Collect().Histograms["latency"].Points[0]
	if len(point.Exemplars) != 2 || point.Exemplars[0].Value != 0.7 || point.Exemplars[1].Value != 20 {
		t.Fatalf("exemplars = %+v, want 0.7 (first bucket) then 20 (overflow)", point.Exemplars)
	}
	if point.Count != 4 {
		t.Errorf("count = %d, want 4", point.Count)
	}
	if again := m.Collect().Histograms["latency"].Points[0].Exemplars; again != nil {
		t.Fatalf("a delta window handed its exemplars over twice: %+v", again)
	}
}

// otlpExemplarSum is the hand-written ExportMetricsServiceRequest of one
// cumulative sum carrying one exemplar: Exemplar's fields in field-number
// order — time_unix_nano (2), span_id (4), trace_id (5), as_int (6) — the
// identifiers hex, as_int a decimal string, between the point's value and
// its attributes.
const otlpExemplarSum = `{"resourceMetrics":[{"resource":{},"scopeMetrics":[{"scope":{},"metrics":[` +
	`{"name":"requests","sum":{"dataPoints":[{` + otlpWindow + `,"asInt":"5",` +
	`"exemplars":[{"timeUnixNano":"1767323050000000000","spanId":"00f067aa0ba902b7","traceId":"4bf92f3577b34da6a3ce929d0e0e4736","asInt":"5"}]` +
	`}],"aggregationTemporality":2,"isMonotonic":true}}` +
	`]}]}]}`

// TestOTLPCarriesASumExemplarAsWritten (#869 acceptance): the encoder's
// bytes for a sum with one exemplar equal the hand-written document; the
// Prometheus connector drops the exemplar and says nothing else changed —
// its exposition is byte-identical with and without one.
func TestOTLPCarriesASumExemplarAsWritten(t *testing.T) {
	t.Parallel()
	snap := coremetrics.SnapshotValue{StartTime: otlpStart, Time: otlpEnd, Sums: map[string]coremetrics.SumMetricValue{
		"requests": {Temporality: coremetrics.TemporalityCumulative, Monotonic: true, Points: []coremetrics.SumValue{{
			Value:     5,
			Exemplars: []coremetrics.ExemplarValue{{Time: exAt, Value: 5, TraceID: exTraceID, SpanID: exSpanID}},
		}}},
	}}
	if got := mustEncode(t, snap); got != otlpExemplarSum {
		t.Fatalf("OTLP/JSON:\n got %s\nwant %s", got, otlpExemplarSum)
	}
	var with, without bytes.Buffer
	if err := svcmetrics.NewPrometheusExporter("p", &with).Export(snap); err != nil {
		t.Fatalf("Prometheus: %v", err)
	}
	bare := snap
	bare.Sums = map[string]coremetrics.SumMetricValue{"requests": {Temporality: coremetrics.TemporalityCumulative, Monotonic: true, Points: []coremetrics.SumValue{{Value: 5}}}}
	if err := svcmetrics.NewPrometheusExporter("p", &without).Export(bare); err != nil {
		t.Fatalf("Prometheus: %v", err)
	}
	if with.String() != without.String() {
		t.Fatalf("the exemplar reached the exposition:\n%s\nvs\n%s", with.String(), without.String())
	}
}
