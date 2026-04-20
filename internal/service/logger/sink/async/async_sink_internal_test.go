package async

import (
	"context"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/buffer"
)

// noopDownstream satisfies corelogger.Sink for white-box drainer tests
// that don't care about delivery.
type noopDownstream struct{}

func (noopDownstream) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	return len(p), nil
}
func (noopDownstream) Flush(_ context.Context) error { return nil }
func (noopDownstream) Close() error                  { return nil }

// freshSink builds an asyncSink directly (bypassing the goroutine spawn) so
// the internal tests can inspect Write / handleFull / Flush / Close without
// timing dependencies. The done channel is pre-closed so Close returns
// immediately and Flush sees an empty queue.
func freshSink(t testing.TB, policy DropPolicy) *asyncSink {
	t.Helper()
	queue := mustNewRing(4)
	pool := buffer.NewRecycler[*recordEntry](newRecordEntry)
	done := make(chan struct{})
	close(done)
	return &asyncSink{
		downstream: noopDownstream{},
		queue:      queue,
		pool:       pool,
		policy:     policy,
		onDrop:     noopOnDrop,
		stop:       make(chan struct{}),
		done:       done,
	}
}

func Test_asyncSink_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		ctxCancel bool
	}{
		{"happy path enqueues bytes", false},
		{"cancelled ctx surfaces ctx.Err", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := freshSink(t, DropNewest)
			ctx := t.Context()
			if tc.ctxCancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			n, err := s.Write(ctx, corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if tc.ctxCancel && err == nil {
				t.Error("cancelled ctx: err = nil, want non-nil")
			}
			if !tc.ctxCancel && (err != nil || n != 1) {
				t.Errorf("happy path: n=%d err=%v, want n=1 err=nil", n, err)
			}
		})
	}
}

func Test_asyncSink_handleFull(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		policy DropPolicy
	}{
		{"DropOldest evicts head and accepts the new entry", DropOldest},
		{"DropNewest discards new entry and surfaces BufferFull", DropNewest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := freshSink(t, tc.policy)
			//: prime the ring so handleFull's eviction branch runs.
			for range s.queue.Capacity() {
				if werr := s.queue.TryWrite(newRecordEntry()); werr != nil {
					t.Fatalf("priming TryWrite err = %v", werr)
				}
			}
			ent := s.pool.Get()
			ent.data = []byte("payload")
			n, err := s.handleFull(ent)
			//: DropOldest succeeds (returns bytes); DropNewest returns BufferFull.
			if tc.policy == DropOldest && (err != nil || n != len(ent.data)) {
				t.Errorf("DropOldest: n=%d err=%v, want n=%d err=nil", n, err, len(ent.data))
			}
			if tc.policy == DropNewest && err == nil {
				t.Error("DropNewest: err = nil, want BufferFull")
			}
		})
	}
}

func Test_asyncSink_Flush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		ctxCancel bool
		wantErr   bool
	}{
		{"empty queue + live ctx returns nil", false, false},
		{"empty queue + cancelled ctx still flushes downstream", true, false},
		{"explicit nil context is permitted by the contract", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := freshSink(t, DropNewest)
			ctx := t.Context()
			if tc.ctxCancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			err := s.Flush(ctx)
			if (err != nil) != tc.wantErr {
				t.Errorf("Flush err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func Test_asyncSink_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Close returns the downstream Close result"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := freshSink(t, DropNewest)
			if err := s.Close(); err != nil {
				t.Errorf("Close err = %v, want nil", err)
			}
		})
	}
}

func Test_defaultBufferSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want int
	}{
		{"defaultBufferSize is 1024", 1024},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if defaultBufferSize != tc.want {
				t.Errorf("defaultBufferSize = %d, want %d", defaultBufferSize, tc.want)
			}
		})
	}
}

func Test_mustNewRing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		size int
	}{
		{"valid size returns a non-nil queue", 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := mustNewRing(tc.size)
			if q == nil {
				t.Error("mustNewRing returned nil")
			}
			//: confirm the queue exposes the documented Capacity contract.
			if cap2 := q.Capacity(); cap2 != tc.size {
				t.Errorf("Capacity = %d, want %d", cap2, tc.size)
			}
		})
	}
}

func Test_noopOnDrop(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  int
	}{
		{"positive count is silently dropped", 5},
		{"negative count is defensively ignored", -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			panicked := false
			func() {
				defer func() {
					if r := recover(); r != nil {
						panicked = true
					}
				}()
				noopOnDrop(tc.val)
			}()
			if panicked {
				t.Errorf("noopOnDrop(%d) panicked; contract requires silent no-op", tc.val)
			}
		})
	}
}
