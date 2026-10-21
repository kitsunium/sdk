package trace_test

import (
	"context"
	"testing"

	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	svctrace "github.com/kitsunium/sdk/internal/service/observe/trace"
)

// benchDiscard accepts every batch and keeps nothing, so the benchmarks price
// the sink rather than an exporter.
type benchDiscard struct{}

// Name names the stub.
func (benchDiscard) Name() coretrace.ExporterName { return "bench-discard" }

// Export keeps nothing.
func (benchDiscard) Export(coretrace.SpansValue) error { return nil }

// newBenchSink builds a sink at the specification's defaults, on the system
// clock, and shuts it down when the benchmark ends.
func newBenchSink(b *testing.B) coretrace.SpanSink {
	b.Helper()
	sink, err := svctrace.NewBatchSink(svctrace.BatchSinkConfig{Exporter: benchDiscard{}})
	if err != nil {
		b.Fatalf("NewBatchSink: %v", err)
	}
	b.Cleanup(func() {
		if shutdownErr := sink.Shutdown(context.Background()); shutdownErr != nil {
			b.Errorf("Shutdown: %v", shutdownErr)
		}
	})
	return sink.Sink()
}

// BenchmarkBatchSinkRecord prices what a span end pays a BatchSink at the
// specification's defaults, with the exporting goroutine draining batches of
// 512 behind it — the steady state of a traced service.
func BenchmarkBatchSinkRecord(b *testing.B) {
	end := newBenchSink(b)
	value := coretrace.SpanValue{Name: "GET", Kind: coretrace.SpanKindServer}
	b.ReportAllocs()
	for b.Loop() {
		end(value)
	}
}

// BenchmarkBatchSinkRecordParallel is the same span end from every P at once:
// the queue's one lock is the contended resource, as Recorder's is.
func BenchmarkBatchSinkRecordParallel(b *testing.B) {
	end := newBenchSink(b)
	value := coretrace.SpanValue{Name: "GET", Kind: coretrace.SpanKindServer}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			end(value)
		}
	})
}
