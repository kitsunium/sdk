//go:build !race

package trace_test

import (
	"context"
	"testing"

	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svctrace "github.com/kitsunium/sdk/internal/service/observe/trace"
)

// TestBatchSinkRecordingAllocatesNothing pins the BatchSink's hot path: a span
// that ends is copied into a slot the queue allocated at construction, so the
// call a request goroutine pays for allocates nothing. The queue is sized past
// the window and the batch with it, so no export starts inside it and every
// call takes the copy, never the drop. It runs in the race-off alloc lane with
// the package's other allocation gates (CLAUDE.md rule 12).
func TestBatchSinkRecordingAllocatesNothing(t *testing.T) {
	const capacity int = 4 * allocRuns
	batchSink, err := svctrace.NewBatchSink(svctrace.BatchSinkConfig{
		Exporter:     benchDiscard{},
		Clock:        clock.NewManualClock(batchEpoch),
		MaxQueueSize: capacity, MaxExportBatchSize: capacity,
	})
	if err != nil {
		t.Fatalf("NewBatchSink: %v", err)
	}
	defer func() {
		if shutdownErr := batchSink.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("Shutdown: %v", shutdownErr)
		}
	}()
	sink := batchSink.Sink()
	value := coretrace.SpanValue{Name: "GET", Kind: coretrace.SpanKindServer}
	got := mallocsOver(allocRuns, func() {
		sink(value)
	})
	reportWindow(t, "spans recorded into a batch sink", got)
	if got.own != 0 {
		t.Errorf("recording %d spans into a batch sink performed %d allocations, want 0", allocRuns, got.own)
	}
	//: and the spans were queued, or the window measured the drop branch.
	if batchSink.Dropped() != 0 || batchSink.Len() < allocRuns {
		t.Errorf("Len = %d, Dropped = %d after the window, want every span queued", batchSink.Len(), batchSink.Dropped())
	}
}
