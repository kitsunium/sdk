package async_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	coreasync "github.com/kitsunium/sdk/internal/core/observe/logger/middleware/async"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/async"
)

// blockRingSize is the ring every Block test saturates: two slots.
const blockRingSize int = 2

// blockedWrite is a Block sink whose ring is full while its drainer is parked
// inside the downstream's Write: the next Write has to wait. The test frees a
// slot by opening the gate, moves the clock, cancels, or closes.
type blockedWrite struct {
	sink    corelogger.Sink
	gate    *gateSink
	clk     *clock.ManualClock
	dropped *atomic.Int64
	closed  bool
}

// saturate builds a Block sink on a manual clock and fills it: one entry
// parks the drainer in the gated downstream, blockRingSize more fill the ring.
func saturate(t *testing.T, maxBlock time.Duration) *blockedWrite {
	t.Helper()
	b := &blockedWrite{
		gate:    &gateSink{started: make(chan struct{}), release: make(chan struct{})},
		clk:     clock.NewManualClock(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)),
		dropped: new(atomic.Int64),
	}
	b.sink = async.New(b.gate, async.Config{
		BufferSize: blockRingSize, Policy: async.Block, MaxBlock: maxBlock, Clock: b.clk,
		OnDrop: func(missed int) { b.dropped.Add(int64(missed)) },
	})
	t.Cleanup(func() {
		if !b.closed {
			b.release()
			swallowAsyncClose(b.sink.Close())
		}
	})
	rec := corelogger.RecordEvent{Level: level.Info}
	if _, err := b.sink.Write(t.Context(), rec, []byte("parked")); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	<-b.gate.started
	for i := range blockRingSize {
		if _, err := b.sink.Write(t.Context(), rec, []byte("queued")); err != nil {
			t.Fatalf("Write %d into a ring with room: %v", i, err)
		}
	}
	return b
}

// release opens the gate once.
func (b *blockedWrite) release() {
	select {
	case <-b.gate.release:
	default:
		close(b.gate.release)
	}
}

// writeAsync starts the Write that has to wait and returns where its outcome
// lands; it returns once that Write has armed its MaxBlock timer, so the test
// knows it is waiting.
func (b *blockedWrite) writeAsync(ctx context.Context) <-chan error {
	return b.writeAsyncNth(ctx, 1)
}

// writeAsyncNth is writeAsync for the nth Write waiting at once.
func (b *blockedWrite) writeAsyncNth(ctx context.Context, nth int) <-chan error {
	result := make(chan error, 1)
	go func() {
		_, err := b.sink.Write(ctx, corelogger.RecordEvent{Level: level.Warn}, []byte("waiting"))
		result <- err
	}()
	b.clk.BlockUntil(nth)
	return result
}

// TestAsync_BlockWaitsForAFreeSlot pins the policy's point: a full ring slows
// the producer instead of losing its entry, and the entry lands once the
// drainer frees a slot.
func TestAsync_BlockWaitsForAFreeSlot(t *testing.T) {
	t.Parallel()
	b := saturate(t, 0)
	result := b.writeAsync(t.Context())
	b.release()
	if err := <-result; err != nil {
		t.Fatalf("Write under Block = %v, want nil once a slot was freed", err)
	}
	if got := b.dropped.Load(); got != 0 {
		t.Fatalf("OnDrop fired %d time(s), want none", got)
	}
}

// TestAsync_BlockReleasesEveryWaiter pins the wake-up hand-off: two Writes
// waiting at once both land once the drainer frees their slots, the first
// passing the wake-up on when it leaves room behind.
func TestAsync_BlockReleasesEveryWaiter(t *testing.T) {
	t.Parallel()
	b := saturate(t, 0)
	first := b.writeAsyncNth(t.Context(), 1)
	second := b.writeAsyncNth(t.Context(), 2)
	b.release()
	for i, result := range []<-chan error{first, second} {
		if err := <-result; err != nil {
			t.Fatalf("waiter %d: Write = %v, want nil", i, err)
		}
	}
	if got := b.dropped.Load(); got != 0 {
		t.Fatalf("OnDrop fired %d time(s), want none", got)
	}
}

// TestAsync_BlockIsBoundedByMaxBlock pins ADR 0031: a zero MaxBlock is the
// one-second default, never forever, and past it the entry is dropped,
// counted and reported.
func TestAsync_BlockIsBoundedByMaxBlock(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		maxBlock time.Duration
		advance  time.Duration
	}{
		{"an unset bound is DefaultMaxBlock", 0, async.DefaultMaxBlock},
		{"a configured bound", 250 * time.Millisecond, 250 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b := saturate(t, tt.maxBlock)
			result := b.writeAsync(t.Context())
			b.clk.Advance(tt.advance)
			if err := <-result; !errors.Is(err, coreasync.BlockTimeout) || !errs.HasCode(err, coreasync.CodeAsyncBlockTimeout) {
				t.Fatalf("Write = %v, want BlockTimeout", err)
			}
			if got := b.dropped.Load(); got != 1 {
				t.Fatalf("OnDrop counted %d, want 1", got)
			}
		})
	}
}

// TestAsync_BlockEndsWithTheCallersContext pins that the caller's context
// bounds the wait as well.
func TestAsync_BlockEndsWithTheCallersContext(t *testing.T) {
	t.Parallel()
	b := saturate(t, 0)
	ctx, cancel := context.WithCancel(t.Context())
	result := b.writeAsync(ctx)
	cancel()
	err := <-result
	if !errs.HasCode(err, coreasync.CodeAsyncCtxCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Write = %v, want CtxCancelled carrying context.Canceled", err)
	}
}

// TestAsync_BlockEndsWithClose pins that Close releases every waiting Write
// with Stopped rather than leaving it to its bound.
func TestAsync_BlockEndsWithClose(t *testing.T) {
	t.Parallel()
	b := saturate(t, 0)
	result := b.writeAsync(t.Context())
	closed := make(chan error, 1)
	b.closed = true
	go func() { closed <- b.sink.Close() }()
	if err := <-result; !errors.Is(err, coreasync.Stopped) {
		t.Fatalf("Write = %v, want Stopped", err)
	}
	b.release()
	if err := <-closed; err != nil {
		t.Fatalf("Close = %v, want nil", err)
	}
}
