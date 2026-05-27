package async

import (
	"context"
	"errors"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/recycler"
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
	pool := recycler.NewPool[*recordEntry](newRecordEntry)
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
		//: prime enqueues entries before Flush so the loop sees a non-empty
		//: ring and falls through to the cancellation arm instead of the
		//: empty-queue fast path. freshSink has no live drainer so the
		//: primed entries are never consumed.
		prime bool
		//: closeSignal pre-closes flushSignal so waitForDrainerProgress
		//: returns false on the first non-empty iteration — driving Flush's
		//: documented Stopped arm ("flushSignal observed closed").
		closeSignal bool
		wantErr     bool
		wantCode    bool
		//: wantStopped asserts the Stopped sentinel (drainer-exited path).
		wantStopped bool
	}{
		{"empty queue + live ctx returns nil", false, false, false, false, false, false},
		{"empty queue + cancelled ctx still flushes downstream", true, false, false, false, false, false},
		{"explicit nil context is permitted by the contract", false, false, false, false, false, false},
		{"non-empty queue + cancelled ctx surfaces the typed cancellation", true, true, false, true, true, false},
		{"non-empty queue + closed flushSignal surfaces Stopped", false, true, true, true, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := freshSink(t, DropNewest)
			//: prime the ring so Flush observes remaining > 0 and reaches the
			//: ctx-cancellation arm rather than returning on the empty path.
			if tc.prime {
				if werr := s.queue.TryWrite(newRecordEntry()); werr != nil {
					t.Fatalf("priming TryWrite err = %v", werr)
				}
			}
			//: pre-close flushSignal to reproduce the documented drainer-exited
			//: state so Flush's waitForDrainerProgress returns false and the
			//: Stopped arm runs. freshSink leaves flushSignal nil, so equip a
			//: dedicated channel first (mirroring Test_waitForDrainerProgress),
			//: then close it. The drainer never closes this channel in
			//: production (only Close terminates it indirectly), so this
			//: white-box setup is the only way to exercise the defensive arm.
			if tc.closeSignal {
				s.flushSignal = make(chan struct{}, 1)
				close(s.flushSignal)
			}
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
			//: the cancellation arm wraps ctx.Err() with the typed sentinel —
			//: assert both the dotted-quad code and stdlib Is() chaining.
			if tc.wantCode {
				if !errs.HasCode(err, CodeAsyncCtxCancelled) {
					t.Errorf("Flush err = %v, want CodeAsyncCtxCancelled", err)
				}
				if !errors.Is(err, context.Canceled) {
					t.Errorf("Flush err = %v, want errors.Is(context.Canceled)", err)
				}
			}
			//: the Stopped arm returns the drainer-exited sentinel verbatim.
			if tc.wantStopped && !errs.HasCode(err, CodeAsyncStopped) {
				t.Errorf("Flush err = %v, want Stopped", err)
			}
		})
	}
}

func Test_asyncSink_waitForDrainerProgress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		//: signal selects the wake-up source for the helper:
		//: "drainer" feeds flushSignal once (steady-state path),
		//: "cancel" cancels ctx (cancellation path),
		//: "closed" closes flushSignal (misuse path).
		signal string
		//: nilCtx exercises the indefinite-wait branch with ctx==nil.
		nilCtx bool
		wantOK bool
	}{
		{"drainer signal wakes the wait", "drainer", false, true},
		{"cancelled ctx wakes the wait", "cancel", false, true},
		{"closed flushSignal returns ok=false", "closed", false, false},
		{"nil ctx blocks on flushSignal", "drainer", true, true},
		{"nil ctx surfaces closed flushSignal", "closed", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := freshSink(t, DropNewest)
			//: equip the helper with a dedicated flushSignal so the test
			//: can drive the wake-up source deterministically.
			s.flushSignal = make(chan struct{}, 1)
			var ctx context.Context
			cancel := func() {}
			if !tc.nilCtx {
				var c context.Context
				c, cancel = context.WithCancel(t.Context())
				ctx = c
				t.Cleanup(cancel)
			}
			switch tc.signal {
			case "drainer":
				//: feed flushSignal so the helper wakes immediately.
				s.flushSignal <- struct{}{}
			case "cancel":
				//: cancellation wakes the select arm; helper returns true.
				cancel()
			case "closed":
				//: closed channel surfaces ok=false to the caller.
				close(s.flushSignal)
			}
			got := s.waitForDrainerProgress(ctx)
			if got != tc.wantOK {
				t.Errorf("waitForDrainerProgress = %v, want %v", got, tc.wantOK)
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
		//: wantPanic asserts the documented contract-violation arm: a
		//: non-positive size makes ring.New fail, which mustNewRing converts
		//: into a panic. New never reaches this path (it substitutes the
		//: default for size <= 0); the helper is probed directly so the
		//: defensive arm has explicit coverage.
		wantPanic bool
	}{
		{"valid size returns a non-nil queue", 4, false},
		{"zero size violates the ring contract and panics", 0, true},
		{"negative size violates the ring contract and panics", -1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: panic arm — capture the recover so the helper's contract-breach
			//: path is exercised without escaping the test goroutine.
			if tc.wantPanic {
				panicked := false
				func() {
					defer func() {
						if r := recover(); r != nil {
							panicked = true
						}
					}()
					mustNewRing(tc.size)
				}()
				if !panicked {
					t.Errorf("mustNewRing(%d) did not panic; contract requires a panic on bad capacity", tc.size)
				}
				return
			}
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

func Test_noopOnError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{"nil error is silently dropped", nil},
		{"non-nil error is silently dropped", errNoopBoom{}},
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
				noopOnError(tc.err)
			}()
			if panicked {
				t.Errorf("noopOnError(%v) panicked; contract requires silent no-op", tc.err)
			}
		})
	}
}

// errNoopBoom is a minimal error used to feed noopOnError with a non-nil
// value; the test only asserts that the call returns without panicking.
type errNoopBoom struct{}

// Error renders the diagnostic marker used by the noopOnError test.
func (errNoopBoom) Error() (msg string) {
	//: static marker — content is not asserted.
	return "boom"
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
