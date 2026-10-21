package trace_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svctrace "github.com/kitsunium/sdk/internal/service/observe/trace"
)

// batchEpoch is where every manual clock of these tests starts.
var batchEpoch = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// batchExporter hands every batch it receives to batches, after waiting on gate
// when one is set — a collector that answers only when the test lets it. A
// pointer, so the sink can compare it.
type batchExporter struct {
	batches chan coretrace.SpansValue
	entered chan struct{}
	gate    chan struct{}
	failure error
}

// newBatchExporter returns an exporter with room for every batch a test makes.
func newBatchExporter(gated bool) *batchExporter {
	e := &batchExporter{batches: make(chan coretrace.SpansValue, 64), entered: make(chan struct{}, 64)}
	if gated {
		e.gate = make(chan struct{})
	}
	return e
}

// Name names the stub.
func (e *batchExporter) Name() coretrace.ExporterName { return "batch-test" }

// Export records the batch once the gate, if any, opens.
func (e *batchExporter) Export(spans coretrace.SpansValue) error {
	select {
	case e.entered <- struct{}{}:
	default:
	}
	if e.gate != nil {
		<-e.gate
	}
	e.batches <- spans
	return e.failure
}

// stallingExporter is a ContextSpanExporter that never answers: it returns only
// when the context it was handed ends.
type stallingExporter struct {
	entered chan struct{}
}

// Name names the stub.
func (e *stallingExporter) Name() coretrace.ExporterName { return "batch-stall" }

// Export is never called on a ContextSpanExporter by the sink.
func (e *stallingExporter) Export(coretrace.SpansValue) error { return nil }

// ExportContext blocks until ctx ends and reports why.
func (e *stallingExporter) ExportContext(ctx context.Context, _ coretrace.SpansValue) error {
	e.entered <- struct{}{}
	<-ctx.Done()
	return context.Cause(ctx)
}

// spans returns n distinguishable finished spans.
func spans(n int) []coretrace.SpanValue {
	out := make([]coretrace.SpanValue, n)
	for i := range out {
		out[i] = coretrace.SpanValue{Name: "span", EndTime: batchEpoch.Add(time.Duration(i))}
	}
	return out
}

// newSink builds a sink on a fresh manual clock, failing the test on a refusal,
// and shuts it down when the test ends.
func newSink(t *testing.T, cfg svctrace.BatchSinkConfig) (*svctrace.BatchSink, *clock.ManualClock) {
	t.Helper()
	clk := clock.NewManualClock(batchEpoch)
	cfg.Clock = clk
	sink, err := svctrace.NewBatchSink(cfg)
	if err != nil {
		t.Fatalf("NewBatchSink: %v", err)
	}
	t.Cleanup(func() {
		if shutdownErr := sink.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("Shutdown: %v", shutdownErr)
		}
	})
	return sink, clk
}

// record ends every span on the sink.
func record(sink *svctrace.BatchSink, batch []coretrace.SpanValue) {
	end := sink.Sink()
	for _, span := range batch {
		end(span)
	}
}

// TestNewBatchSinkRefusesAMissingExporter pins the ADR 0031 answer: a batch
// with nowhere to go has no default, so the sink is refused, not built inert.
func TestNewBatchSinkRefusesAMissingExporter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		exporter coretrace.SpanExporter
	}{
		{"no exporter", nil},
		{"a typed nil", (*batchExporter)(nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sink, err := svctrace.NewBatchSink(svctrace.BatchSinkConfig{Exporter: tt.exporter})
			if sink != nil || !errors.Is(err, coretrace.BatchExporterMissing) {
				t.Fatalf("NewBatchSink = %v, %v; want nil, BatchExporterMissing", sink, err)
			}
		})
	}
}

// TestBatchSinkExportsAFullBatchAtOnce pins the fill trigger at the
// specification's default size: 512 spans leave without the clock moving.
func TestBatchSinkExportsAFullBatchAtOnce(t *testing.T) {
	t.Parallel()
	exporter := newBatchExporter(false)
	sink, _ := newSink(t, svctrace.BatchSinkConfig{Exporter: exporter})
	record(sink, spans(svctrace.DefaultBatchMaxExportBatchSize))
	got := <-exporter.batches
	if len(got.Spans) != svctrace.DefaultBatchMaxExportBatchSize {
		t.Fatalf("batch of %d, want %d", len(got.Spans), svctrace.DefaultBatchMaxExportBatchSize)
	}
	if got.Scope.Name == "" {
		t.Error("the batch carries no normalised scope")
	}
}

// TestBatchSinkExportsAPartialBatchAfterTheScheduledDelay pins the timer
// trigger, on the injected clock: three spans wait for the delay, then leave
// together, in the order they ended.
func TestBatchSinkExportsAPartialBatchAfterTheScheduledDelay(t *testing.T) {
	t.Parallel()
	exporter := newBatchExporter(false)
	sink, clk := newSink(t, svctrace.BatchSinkConfig{Exporter: exporter})
	want := spans(3)
	record(sink, want)
	clk.BlockUntil(1)
	clk.Advance(svctrace.DefaultBatchScheduledDelay)
	got := <-exporter.batches
	if len(got.Spans) != len(want) {
		t.Fatalf("batch of %d, want %d", len(got.Spans), len(want))
	}
	for i := range want {
		if !got.Spans[i].EndTime.Equal(want[i].EndTime) {
			t.Fatalf("span %d out of order", i)
		}
	}
}

// TestBatchSinkClampsTheBatchToTheQueue pins the specification's "MUST be less
// than or equal to maxQueueSize": a batch size above the queue is the queue,
// so a full queue is a full batch and leaves at once.
func TestBatchSinkClampsTheBatchToTheQueue(t *testing.T) {
	t.Parallel()
	exporter := newBatchExporter(false)
	sink, _ := newSink(t, svctrace.BatchSinkConfig{Exporter: exporter, MaxQueueSize: 2, MaxExportBatchSize: 10})
	record(sink, spans(2))
	if got := <-exporter.batches; len(got.Spans) != 2 {
		t.Fatalf("batch of %d, want the 2 the queue holds", len(got.Spans))
	}
}

// TestBatchSinkDropsAndCountsPastTheQueue pins the bound: while the exporter
// holds a batch, the queue fills, and the span that finds it full is dropped
// and counted — never a block, never a silent loss.
func TestBatchSinkDropsAndCountsPastTheQueue(t *testing.T) {
	t.Parallel()
	exporter := newBatchExporter(true)
	sink, _ := newSink(t, svctrace.BatchSinkConfig{Exporter: exporter, MaxQueueSize: 2, MaxExportBatchSize: 2})
	record(sink, spans(2))
	<-exporter.entered
	record(sink, spans(3))
	if got := sink.Dropped(); got != 1 {
		t.Fatalf("Dropped = %d, want 1", got)
	}
	if got := sink.Len(); got != 2 {
		t.Fatalf("Len = %d, want 2", got)
	}
	close(exporter.gate)
	if err := sink.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}
	if first, second := <-exporter.batches, <-exporter.batches; len(first.Spans)+len(second.Spans) != 4 {
		t.Fatalf("exported %d spans, want 4", len(first.Spans)+len(second.Spans))
	}
}

// TestBatchSinkBoundsAnExportOnItsClock pins exportTimeout: an exporter that
// never answers is cut off when the sink's clock reaches the bound, its spans
// counted as failed and the failure reported, typed.
func TestBatchSinkBoundsAnExportOnItsClock(t *testing.T) {
	t.Parallel()
	exporter := &stallingExporter{entered: make(chan struct{}, 1)}
	failures := make(chan error, 1)
	sink, clk := newSink(t, svctrace.BatchSinkConfig{
		Exporter: exporter, MaxExportBatchSize: 1,
		OnError: func(err error) { failures <- err },
	})
	record(sink, spans(1))
	<-exporter.entered
	//: the scheduled delay and the export bound are both armed.
	clk.BlockUntil(2)
	clk.Advance(svctrace.DefaultBatchExportTimeout)
	err := <-failures
	if !errs.HasCode(err, coretrace.CodeExportFailed) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("OnError = %v, want ExportFailed carrying the deadline", err)
	}
	if got := sink.Failed(); got != 1 {
		t.Fatalf("Failed = %d, want 1", got)
	}
}

// TestBatchSinkForceFlushExportsEverythingQueued pins ForceFlush: every span
// queued at the call leaves, in batches, before it returns.
func TestBatchSinkForceFlushExportsEverythingQueued(t *testing.T) {
	t.Parallel()
	exporter := newBatchExporter(false)
	sink, _ := newSink(t, svctrace.BatchSinkConfig{Exporter: exporter, MaxExportBatchSize: 4, MaxQueueSize: 16})
	record(sink, spans(3))
	if err := sink.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}
	if got := sink.Len(); got != 0 {
		t.Fatalf("Len after ForceFlush = %d, want 0", got)
	}
	if got := <-exporter.batches; len(got.Spans) != 3 {
		t.Fatalf("batch of %d, want 3", len(got.Spans))
	}
}

// TestBatchSinkForceFlushReturnsUnderSteadyTraffic pins that a flush is
// bounded by what was queued when it began: spans that keep ending while it
// exports wait for a later export, so ForceFlush returns instead of chasing a
// queue that never empties.
func TestBatchSinkForceFlushReturnsUnderSteadyTraffic(t *testing.T) {
	t.Parallel()
	sink, _ := newSink(t, svctrace.BatchSinkConfig{Exporter: benchDiscard{}, MaxExportBatchSize: 4, MaxQueueSize: 64})
	stop := make(chan struct{})
	traffic := make(chan struct{})
	go func() {
		defer close(traffic)
		end := sink.Sink()
		for {
			select {
			case <-stop:
				return
			default:
				end(coretrace.SpanValue{Name: "steady"})
			}
		}
	}()
	record(sink, spans(8))
	if err := sink.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}
	close(stop)
	<-traffic
}

// TestBatchSinkForceFlushReturnsTheExportFailure pins that a flush reports
// what the exporter said, typed.
func TestBatchSinkForceFlushReturnsTheExportFailure(t *testing.T) {
	t.Parallel()
	exporter := newBatchExporter(false)
	exporter.failure = errors.New("collector refused")
	sink, _ := newSink(t, svctrace.BatchSinkConfig{Exporter: exporter})
	record(sink, spans(2))
	err := sink.ForceFlush(context.Background())
	if !errs.HasCode(err, coretrace.CodeExportFailed) || !errors.Is(err, exporter.failure) {
		t.Fatalf("ForceFlush = %v, want ExportFailed carrying the cause", err)
	}
	if got := sink.Failed(); got != 2 {
		t.Fatalf("Failed = %d, want 2", got)
	}
}

// TestBatchSinkWaitsAreBoundedByTheCallersContext pins that ForceFlush and
// Shutdown never outlast their context, whatever the exporter does.
func TestBatchSinkWaitsAreBoundedByTheCallersContext(t *testing.T) {
	t.Parallel()
	exporter := newBatchExporter(true)
	clk := clock.NewManualClock(batchEpoch)
	sink, err := svctrace.NewBatchSink(svctrace.BatchSinkConfig{Exporter: exporter, MaxExportBatchSize: 1, Clock: clk})
	if err != nil {
		t.Fatalf("NewBatchSink: %v", err)
	}
	record(sink, spans(1))
	<-exporter.entered
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	if flushErr := sink.ForceFlush(ended); !errors.Is(flushErr, coretrace.BatchFlushIncomplete) || !errors.Is(flushErr, context.Canceled) {
		t.Fatalf("ForceFlush = %v, want BatchFlushIncomplete carrying the context's end", flushErr)
	}
	if shutdownErr := sink.Shutdown(ended); !errors.Is(shutdownErr, coretrace.BatchFlushIncomplete) {
		t.Fatalf("Shutdown = %v, want BatchFlushIncomplete", shutdownErr)
	}
	close(exporter.gate)
	//: the second call waits for the same end and has nothing to report.
	if shutdownErr := sink.Shutdown(context.Background()); shutdownErr != nil {
		t.Fatalf("second Shutdown = %v, want nil", shutdownErr)
	}
}

// TestBatchSinkShutdownFlushesThenRefuses pins Shutdown: what was queued
// leaves, a span ending afterwards is dropped and counted, a second Shutdown
// and a ForceFlush have nothing to do.
func TestBatchSinkShutdownFlushesThenRefuses(t *testing.T) {
	t.Parallel()
	exporter := newBatchExporter(false)
	clk := clock.NewManualClock(batchEpoch)
	sink, err := svctrace.NewBatchSink(svctrace.BatchSinkConfig{Exporter: exporter, Clock: clk})
	if err != nil {
		t.Fatalf("NewBatchSink: %v", err)
	}
	record(sink, spans(2))
	if shutdownErr := sink.Shutdown(context.Background()); shutdownErr != nil {
		t.Fatalf("Shutdown: %v", shutdownErr)
	}
	if got := <-exporter.batches; len(got.Spans) != 2 {
		t.Fatalf("batch of %d, want 2", len(got.Spans))
	}
	record(sink, spans(1))
	if got := sink.Dropped(); got != 1 {
		t.Fatalf("Dropped after Shutdown = %d, want 1", got)
	}
	if again := sink.Shutdown(context.Background()); again != nil {
		t.Fatalf("second Shutdown = %v, want nil", again)
	}
	if flushErr := sink.ForceFlush(context.Background()); flushErr != nil {
		t.Fatalf("ForceFlush after Shutdown = %v, want nil", flushErr)
	}
}

// TestBatchSinkRecordsConcurrently drives the sink from many goroutines: every
// span is either exported or counted as dropped.
func TestBatchSinkRecordsConcurrently(t *testing.T) {
	t.Parallel()
	const writers, each = 8, 100
	exporter := newBatchExporter(false)
	exporter.batches = make(chan coretrace.SpansValue, writers*each)
	clk := clock.NewManualClock(batchEpoch)
	sink, err := svctrace.NewBatchSink(svctrace.BatchSinkConfig{Exporter: exporter, Clock: clk, MaxQueueSize: 64, MaxExportBatchSize: 16})
	if err != nil {
		t.Fatalf("NewBatchSink: %v", err)
	}
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() { record(sink, spans(each)) })
	}
	wg.Wait()
	if shutdownErr := sink.Shutdown(context.Background()); shutdownErr != nil {
		t.Fatalf("Shutdown: %v", shutdownErr)
	}
	close(exporter.batches)
	exported := 0
	for batch := range exporter.batches {
		exported += len(batch.Spans)
	}
	if total := uint64(exported) + sink.Dropped(); total != writers*each {
		t.Fatalf("exported %d + dropped %d = %d, want %d", exported, sink.Dropped(), total, writers*each)
	}
}
