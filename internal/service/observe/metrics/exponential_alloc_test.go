//go:build !race

// Package metrics — the allocation gate behind the exponential histogram's
// and the exemplars' hot-path claim (#869). Race-off lane only, as
// meter_alloc_test.go explains: //internal/service/observe/metrics:metrics_test
// is listed in tools/alloc-lane-targets.txt.
package metrics

import (
	"context"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
)

// TestRecordingIsAllocationFree pins #869's hot path: recording into an
// exponential histogram — across a scale change — and recording an exemplar
// under a sampled span, once the reservoir exists, allocate nothing.
func TestRecordingIsAllocationFree(t *testing.T) {
	sampled := coretrace.ContextWithSpanContext(context.Background(), coretrace.SpanContextValue{
		TraceID: coretrace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:  coretrace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		Flags:   coretrace.TraceFlags(1),
	})
	m := newMemMeter(MeterConfig{})
	exp := m.ExponentialHistogram("latency", 8)
	//: the assertions are made once, outside the window: the runtime builds
	//: a type-assertion cache lazily, about one call in 1024, an allocation no
	//: line here owns (meter_alloc_test.go, allocRuntimeWarmup).
	expCtx := exp.(coremetrics.ContextHistogram)
	hist := m.Histogram("explicit", []float64{1, 10}).(coremetrics.ContextHistogram)
	sum := m.Counter("requests").(coremetrics.ContextCounter)
	//: the first sampled measurement allocates each reservoir, once.
	expCtx.RecordContext(sampled, 1)
	hist.RecordContext(sampled, 1)
	sum.AddContext(sampled, 1)
	value := 1.0
	got := mallocsOver(allocRuns, func() {
		//: a range wider than 8 buckets at every scale forces downscales.
		value *= 1.7
		exp.Record(value)
		expCtx.RecordContext(sampled, -value)
		hist.RecordContext(sampled, value)
		sum.AddContext(sampled, 2)
	})
	reportWindow(t, "exponential records and exemplars", got)
	if got.own != 0 {
		t.Errorf("%d rounds of recording performed %d allocations, want 0", allocRuns, got.own)
	}
}
