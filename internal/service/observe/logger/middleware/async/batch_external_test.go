package async_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/async"
)

// errBatch is the refusal batchGate's WriteBatch answers with when told to fail.
var errBatch = errors.New("test batch failed")

// batchGate is a BatchSink whose first Write blocks until release is closed,
// so the test can queue a backlog behind it and see how the drainer hands
// that backlog over. It records every call's size, and answers WriteBatch
// with (failAt, errBatch) when failAt is not negative.
type batchGate struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	failAt  int
	mu      sync.Mutex
	writes  int
	batches []int
}

func newBatchGate(failAt int) *batchGate {
	return &batchGate{started: make(chan struct{}), release: make(chan struct{}), failAt: failAt}
}

func (g *batchGate) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	g.once.Do(func() {
		close(g.started)
		<-g.release
	})
	g.mu.Lock()
	g.writes++
	g.mu.Unlock()
	return len(p), nil
}

func (g *batchGate) WriteBatch(_ context.Context, recs []corelogger.RecordEvent, payloads [][]byte) (int, error) {
	if len(recs) != len(payloads) {
		panic("WriteBatch: recs and payloads differ in length")
	}
	g.mu.Lock()
	g.batches = append(g.batches, len(recs))
	g.mu.Unlock()
	if g.failAt >= 0 {
		return g.failAt, errBatch
	}
	return len(recs), nil
}

func (g *batchGate) Flush(context.Context) error { return nil }
func (g *batchGate) Close() error                { return nil }

// counts reports the single Writes and the record count of every WriteBatch.
func (g *batchGate) counts() (writes int, batches []int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.writes, append([]int(nil), g.batches...)
}

// queueBehindGate writes one record (which the drainer takes alone and blocks
// on), then backlog more while it is blocked, then releases it.
func queueBehindGate(t *testing.T, s corelogger.Sink, g *batchGate, backlog int) {
	t.Helper()
	rec := corelogger.RecordEvent{Level: level.Info}
	if _, err := s.Write(t.Context(), rec, []byte("first\n")); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	<-g.started
	for range backlog {
		if _, err := s.Write(t.Context(), rec, []byte("record\n")); err != nil {
			t.Fatalf("backlog Write: %v", err)
		}
	}
	close(g.release)
}

// TestBatchMaxHandsABatchSinkTheBacklogInFewCalls: with BatchMax 16 a backlog
// of 20 records queued behind a blocked first Write reaches a BatchSink in two
// WriteBatch calls (16 and 4), every record exactly once.
func TestBatchMaxHandsABatchSinkTheBacklogInFewCalls(t *testing.T) {
	t.Parallel()
	g := newBatchGate(-1)
	s := async.New(g, async.Config{BufferSize: 64, BatchMax: 16})
	queueBehindGate(t, s, g, 20)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	writes, batches := g.counts()
	total := writes
	for _, n := range batches {
		total += n
		if n > 16 {
			t.Errorf("a WriteBatch carried %d records, past BatchMax 16", n)
		}
	}
	if total != 21 {
		t.Fatalf("delivered %d records (writes %d, batches %v), want 21", total, writes, batches)
	}
	if len(batches) == 0 || batches[0] != 16 {
		t.Fatalf("batches = %v, want the backlog's first WriteBatch to carry 16", batches)
	}
}

// TestBatchMaxWritesRecordByRecordToAPlainSink: a downstream that is not a
// BatchSink receives exactly one Write per record whatever BatchMax says.
func TestBatchMaxWritesRecordByRecordToAPlainSink(t *testing.T) {
	t.Parallel()
	down := &recordingSink{}
	s := async.New(down, async.Config{BufferSize: 256, BatchMax: 16})
	rec := corelogger.RecordEvent{Level: level.Info}
	for range 200 {
		if _, err := s.Write(t.Context(), rec, []byte("x\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := down.writes.Load(); got != 200 {
		t.Fatalf("plain sink saw %d Writes, want one per record (200)", got)
	}
}

// TestZeroBatchMaxNeverCallsWriteBatch: the zero Config keeps the
// record-at-a-time delivery even to a BatchSink.
func TestZeroBatchMaxNeverCallsWriteBatch(t *testing.T) {
	t.Parallel()
	g := newBatchGate(-1)
	s := async.New(g, async.Config{BufferSize: 64})
	queueBehindGate(t, s, g, 10)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if writes, batches := g.counts(); writes != 11 || len(batches) != 0 {
		t.Fatalf("writes %d, batches %v; want 11 Writes and no WriteBatch", writes, batches)
	}
}

// TestFailedBatchReportsOnErrorPerUndeliveredRecord: a WriteBatch that
// delivered 2 of its records and failed fires OnError once for each of the
// others, not once for the batch.
func TestFailedBatchReportsOnErrorPerUndeliveredRecord(t *testing.T) {
	t.Parallel()
	g := newBatchGate(2)
	var reported atomic.Int64
	s := async.New(g, async.Config{BufferSize: 64, BatchMax: 16, OnError: func(err error) {
		if !errors.Is(err, errBatch) {
			t.Errorf("OnError got %v, want the batch's refusal", err)
		}
		reported.Add(1)
	}})
	queueBehindGate(t, s, g, 5)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, batches := g.counts()
	want := int64(0)
	for _, n := range batches {
		want += int64(n - min(2, n))
	}
	if got := reported.Load(); got != want || want == 0 {
		t.Fatalf("OnError fired %d times for batches %v, want %d (one per undelivered record)", got, batches, want)
	}
}
