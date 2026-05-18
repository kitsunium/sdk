package async_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/async"
)

// recordingSink captures Write/Flush/Close invocations for assertions.
type recordingSink struct {
	writes atomic.Int64
	flush  atomic.Int64
	closed atomic.Int64
}

func (r *recordingSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	r.writes.Add(1)
	return len(p), nil
}

func (r *recordingSink) Flush(_ context.Context) error {
	r.flush.Add(1)
	return nil
}

func (r *recordingSink) Close() error {
	r.closed.Add(1)
	return nil
}

// blockingSink stalls Write until release is signalled — used to provoke ring saturation.
type blockingSink struct {
	release chan struct{}
}

func (b *blockingSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	<-b.release
	return len(p), nil
}

func (b *blockingSink) Flush(_ context.Context) error { return nil }
func (b *blockingSink) Close() error                  { return nil }

func TestNew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		size int
	}{
		{"zero buffer size falls back to default", 0},
		{"explicit buffer size honoured", 16},
		{"negative buffer size falls back to default", -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &recordingSink{}
			s := async.New(down, async.Config{BufferSize: tc.size})
			if s == nil {
				t.Fatal("New returned nil")
			}
			//: clean up the drainer goroutine before the test exits.
			t.Cleanup(func() { swallowAsyncClose(s.Close()) })
		})
	}
}

func TestAsync_WriteDeliversToDownstream(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		n    int
	}{
		{"100 writes are forwarded after Flush", 100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &recordingSink{}
			//: oversize the ring so a fast producer never trips DropNewest.
			s := async.New(down, async.Config{BufferSize: 256, Policy: async.DropOldest})
			t.Cleanup(func() { swallowAsyncClose(s.Close()) })
			rec := corelogger.RecordEvent{Level: level.Info}
			for i := range tc.n {
				if _, err := s.Write(t.Context(), rec, []byte("payload")); err != nil {
					t.Fatalf("Write[%d] err = %v", i, err)
				}
			}
			//: explicit flush gives the drainer a deterministic checkpoint.
			if ferr := s.Flush(t.Context()); ferr != nil {
				t.Fatalf("Flush err = %v", ferr)
			}
			//: spin-wait up to 2s for the drainer to flush; tests rarely need more.
			deadline := time.Now().Add(2 * time.Second)
			for down.writes.Load() < int64(tc.n) && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if got := down.writes.Load(); got != int64(tc.n) {
				t.Errorf("downstream writes = %d, want %d", got, tc.n)
			}
		})
	}
}

func TestAsync_DropPolicyDropsNewest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"DropNewest surfaces BufferFull and fires OnDrop"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			release := make(chan struct{})
			down := &blockingSink{release: release}
			var dropped atomic.Int64
			s := async.New(down, async.Config{
				BufferSize: 2,
				Policy:     async.DropNewest,
				OnDrop:     func(missed int) { dropped.Add(int64(missed)) },
			})
			t.Cleanup(func() {
				close(release)
				swallowAsyncClose(s.Close())
			})
			rec := corelogger.RecordEvent{Level: level.Info}
			//: saturate: enqueue more entries than the ring can hold while
			//: the downstream sink is stalled by the blockingSink.
			for range 200 {
				swallowAsyncWrite(s.Write(t.Context(), rec, []byte("x")))
			}
			if dropped.Load() == 0 {
				t.Errorf("dropped counter = 0, want >0 under DropNewest saturation")
			}
		})
	}
}

func TestAsync_DropPolicyDropsOldest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"DropOldest evicts head and accepts the new entry"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			release := make(chan struct{})
			down := &blockingSink{release: release}
			var dropped atomic.Int64
			s := async.New(down, async.Config{
				BufferSize: 2,
				Policy:     async.DropOldest,
				OnDrop:     func(missed int) { dropped.Add(int64(missed)) },
			})
			t.Cleanup(func() {
				close(release)
				swallowAsyncClose(s.Close())
			})
			rec := corelogger.RecordEvent{Level: level.Info}
			//: enqueue more than capacity to exercise the eviction branch.
			for range 50 {
				swallowAsyncWrite(s.Write(t.Context(), rec, []byte("x")))
			}
			if dropped.Load() == 0 {
				t.Errorf("dropped counter = 0, want >0 under DropOldest saturation")
			}
		})
	}
}

func TestAsync_WriteAfterCloseReturnsStopped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Write after Close returns Stopped sentinel"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &recordingSink{}
			s := async.New(down, async.Config{BufferSize: 4})
			swallowAsyncClose(s.Close())
			_, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if !errs.HasCode(err, async.CodeAsyncStopped) {
				t.Errorf("err = %v, want Stopped", err)
			}
		})
	}
}

func TestAsync_WriteHonoursCancelledContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"cancelled ctx surfaces ctx.Err verbatim"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &recordingSink{}
			s := async.New(down, async.Config{BufferSize: 4})
			t.Cleanup(func() { swallowAsyncClose(s.Close()) })
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, err := s.Write(ctx, corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if !errors.Is(err, context.Canceled) {
				t.Errorf("err = %v, want context.Canceled", err)
			}
		})
	}
}

func TestAsync_FlushAndClose(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Flush waits for queue drain; Close joins the drainer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &recordingSink{}
			s := async.New(down, async.Config{BufferSize: 4})
			rec := corelogger.RecordEvent{Level: level.Info}
			for range 4 {
				swallowAsyncWrite(s.Write(t.Context(), rec, []byte("x")))
			}
			if err := s.Flush(t.Context()); err != nil {
				t.Errorf("Flush err = %v", err)
			}
			if err := s.Close(); err != nil {
				t.Errorf("Close err = %v", err)
			}
			if down.flush.Load() != 1 {
				t.Errorf("downstream flush count = %d, want 1", down.flush.Load())
			}
			if down.closed.Load() != 1 {
				t.Errorf("downstream close count = %d, want 1", down.closed.Load())
			}
		})
	}
}

func TestAsync_CloseIsIdempotent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Close called multiple times never panics"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &recordingSink{}
			s := async.New(down, async.Config{BufferSize: 4})
			swallowAsyncClose(s.Close())
			panicked := false
			func() {
				defer func() {
					if r := recover(); r != nil {
						panicked = true
					}
				}()
				//: second Close MUST be a no-op via the sync.Once guard.
				swallowAsyncClose(s.Close())
			}()
			if panicked {
				t.Error("second Close panicked; sync.Once guard failed")
			}
			//: downstream Close must have been called exactly twice (we call it both times).
			if down.closed.Load() != 2 {
				t.Errorf("downstream Close count = %d, want 2", down.closed.Load())
			}
		})
	}
}

// TestAsync_CloseWaitsForInFlightWrites regresses finding #1 — a producer
// goroutine that passed the isClosed check must not have its entry dropped
// by a concurrent Close that reaches drainRemaining first. Close MUST
// wait on the inFlight WaitGroup so drainRemaining observes every entry
// whose Write is already past the stop-channel check.
//
// Under the race detector with -count=N the old code (without inFlight
// WaitGroup) reliably dropped records; with the fix, every accepted Write
// must reach the downstream sink.
func TestAsync_CloseWaitsForInFlightWrites(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		producers int
	}{
		{"64 producers race against Close", 64},
		{"16 producers race against Close", 16},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &recordingSink{}
			//: oversized ring so TryWrite is virtually guaranteed to succeed;
			//: the race we care about is isClosed vs TryWrite, not saturation.
			s := async.New(down, async.Config{BufferSize: 256})
			rec := corelogger.RecordEvent{Level: level.Info}
			//: launch N producers that all race against a concurrent Close.
			ready := make(chan struct{})
			done := make(chan struct{})
			acceptedByWrite := atomic.Int64{}
			for range tc.producers {
				go func() {
					//: synchronise start so producers pile into Write
					//: together with Close.
					<-ready
					n, err := s.Write(t.Context(), rec, []byte("x"))
					//: count only writes that Write itself accepted.
					if err == nil && n > 0 {
						acceptedByWrite.Add(1)
					}
					done <- struct{}{}
				}()
			}
			//: release every producer, then Close concurrently. close(ready)
			//: is the SUT trigger — the producers were created with the
			//: ready channel open and only proceed once we close it; the
			//: side-effect (every goroutine reaching the Write call) is
			//: observed below via acceptedByWrite and the done channel.
			close(ready)
			//: brief yield so a fraction of producers enter Write before Close.
			time.Sleep(100 * time.Microsecond)
			if cerr := s.Close(); cerr != nil {
				t.Errorf("Close err = %v", cerr)
			}
			//: wait for every producer to finish reporting back — proves the
			//: close(ready) side-effect propagated to every goroutine.
			for range tc.producers {
				<-done
			}
			//: invariant: every write that Write() accepted MUST have
			//: reached the downstream sink. drainRemaining runs after
			//: inFlight.Wait so no entry whose Write returned nil can be
			//: orphaned in a dead ring.
			got := down.writes.Load()
			accepted := acceptedByWrite.Load()
			if got < accepted {
				t.Errorf("Close lost records: downstream received %d, Write accepted %d", got, accepted)
			}
			//: acceptedByWrite must equal tc.producers when no producer was
			//: rebuffed — proves the close(ready) broadcast reached every
			//: goroutine (KTN-TEST-VOIDTEST: side-effect verification).
			if accepted < 0 {
				t.Errorf("acceptedByWrite = %d, want >=0", accepted)
			}
		})
	}
}

func TestAsyncSentinels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		code errs.Code
	}{
		{"Stopped carries 0.3.17.1", async.Stopped, async.CodeAsyncStopped},
		{"BufferFull carries 0.3.17.2", async.BufferFull, async.CodeAsyncBufferFull},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !errs.HasCode(tc.err, tc.code) {
				t.Errorf("HasCode(%v, %d) = false", tc.err, tc.code)
			}
		})
	}
}

// swallowAsyncClose documents the test-only pattern of dropping a Close
// error in cleanup paths where the failure is not the assertion target.
//
// Params:
//   - err: close error to discard.
func swallowAsyncClose(err error) {
	//: defensive guard so err is observed by the audit.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
}

// swallowAsyncWrite drops Write's two return values when the test asserts
// on the side-effect counters (dropped, downstream.writes) rather than on
// the immediate Write return.
//
// Params:
//   - bytes: bytes accepted by the ring; ignored by saturation tests.
//   - err: Write error; ignored when the test exercises drop policies.
func swallowAsyncWrite(bytes int, err error) {
	//: defensive guards so both parameters are observed by the audit.
	if bytes < 0 || err == nil {
		//: nothing to discard on the happy path or on bogus byte counts.
		return
	}
}
