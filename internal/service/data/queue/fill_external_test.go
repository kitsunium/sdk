package queue_test

import (
	"testing"
	"time"

	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// TestFillReadyForTestWritesWhatABrokerReceives holds the scale benchmark's
// backlog to the broker's own reading: every message FillReadyForTest wrote is
// delivered once, in the order it was written, with its payload — a backlog
// the grammar refused would leave the benchmark timing empty Receives.
func TestFillReadyForTestWritesWhatABrokerReceives(t *testing.T) {
	t.Parallel()
	const count, size int = 12, 64
	dir := t.TempDir()
	broker, err := svcqueue.NewFile(svcqueue.FileConfig{Dir: dir, Policy: benchPolicy()})
	if err != nil {
		t.Fatalf("NewFile() = %v", err)
	}
	if fillErr := svcqueue.FillReadyForTest(dir, count, size, time.Now().Add(-time.Hour)); fillErr != nil {
		t.Fatalf("FillReadyForTest() = %v", fillErr)
	}
	previous := time.Time{}
	for index := range count {
		deliveries, receiveErr := broker.Receive(t.Context(), 1)
		if receiveErr != nil || len(deliveries) != 1 {
			t.Fatalf("Receive() #%d = %d deliveries, %v; want 1, nil", index, len(deliveries), receiveErr)
		}
		message := deliveries[0].Message
		if len(message.Payload) != size {
			t.Fatalf("message #%d carries %d bytes, want %d", index, len(message.Payload), size)
		}
		if !message.EnqueuedAt.After(previous) {
			t.Fatalf("message #%d enqueued at %v, not after %v: the backlog is not in its written order", index, message.EnqueuedAt, previous)
		}
		previous = message.EnqueuedAt
		if ackErr := broker.Ack(t.Context(), deliveries[0].Lease.Receipt); ackErr != nil {
			t.Fatalf("Ack() #%d = %v", index, ackErr)
		}
	}
	if rest, receiveErr := broker.Receive(t.Context(), 1); receiveErr != nil || len(rest) != 0 {
		t.Fatalf("Receive() after the backlog = %d deliveries, %v; want 0, nil", len(rest), receiveErr)
	}
}
