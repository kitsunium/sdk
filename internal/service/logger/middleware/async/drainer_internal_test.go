package async

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/buffer"
)

// countingDownstream counts every Write so drainer tests can assert that
// the goroutine actually consumed the queue.
type countingDownstream struct {
	writes atomic.Int64
}

func (c *countingDownstream) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	c.writes.Add(1)
	return len(p), nil
}
func (c *countingDownstream) Flush(_ context.Context) error { return nil }
func (c *countingDownstream) Close() error                  { return nil }

// liveSink builds an asyncSink with a real drainer goroutine running.
func liveSink(t testing.TB, down corelogger.Sink) *asyncSink {
	t.Helper()
	s := &asyncSink{
		downstream: down,
		queue:      mustNewRing(8),
		pool:       buffer.NewRecycler[*recordEntry](newRecordEntry),
		policy:     DropNewest,
		onDrop:     noopOnDrop,
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	//: spawn the drainer the same way New does.
	go s.drain()
	return s
}

func Test_asyncSink_drain(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		n    int
	}{
		{"drain consumes 4 entries before Close returns", 4},
		{"drain consumes a single entry", 1},
		{"drain handles immediate Close on empty queue", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &countingDownstream{}
			s := liveSink(t, down)
			rec := corelogger.RecordEvent{Level: level.Info}
			//: enqueue tc.n entries; the drainer should pick them up.
			for range tc.n {
				if _, err := s.Write(t.Context(), rec, []byte("x")); err != nil {
					t.Fatalf("Write err = %v", err)
				}
			}
			//: spin-wait for the drainer to process the batch.
			deadline := time.Now().Add(2 * time.Second)
			for down.writes.Load() < int64(tc.n) && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if got := down.writes.Load(); got != int64(tc.n) {
				t.Errorf("downstream writes = %d, want %d", got, tc.n)
			}
			if err := s.Close(); err != nil {
				t.Errorf("Close err = %v", err)
			}
		})
	}
}

func Test_asyncSink_forward(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"forward delivers the entry and recycles it"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &countingDownstream{}
			s := freshSink(t, DropNewest)
			s.downstream = down
			ent := s.pool.Get()
			ent.rec = corelogger.RecordEvent{Level: level.Info}
			ent.data = append(ent.data[:0], "payload"...)
			s.forward(ent)
			if down.writes.Load() != 1 {
				t.Errorf("downstream writes = %d, want 1", down.writes.Load())
			}
		})
	}
}

func Test_asyncSink_drainRemaining(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		n    int
	}{
		{"drainRemaining flushes every queued entry", 3},
		{"drainRemaining is a no-op on an empty queue", 0},
		{"drainRemaining handles a single entry", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &countingDownstream{}
			s := freshSink(t, DropNewest)
			s.downstream = down
			//: pre-fill the ring with tc.n entries.
			for range tc.n {
				ent := s.pool.Get()
				ent.data = append(ent.data[:0], "x"...)
				if err := s.queue.TryWrite(ent); err != nil {
					t.Fatalf("TryWrite err = %v", err)
				}
			}
			s.drainRemaining()
			if down.writes.Load() != int64(tc.n) {
				t.Errorf("downstream writes = %d, want %d", down.writes.Load(), tc.n)
			}
		})
	}
}
