// Package resilience — the timeout policy's Run.
package resilience

import (
	"context"
	"errors"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
)

// Test_timeoutRunner_Run pins the deadline's authority AND its boundary.
//
// The authority half: an operation that IGNORES its context and reports success
// after the deadline expired must still surface TimeoutExceeded. An earlier
// version only consulted the derived context when the operation returned an
// error, so a ctx-deaf operation returning nil made Run report success and
// silently void the policy. The documented degradation for such an operation is
// "precision drops to op granularity" — not "the deadline is abandoned".
//
// The boundary half: a PARENT cancellation is not a deadline. Relabelling it
// TimeoutExceeded would tell a caller shutting down that its dependency was
// slow, which is a different problem with a different fix.
func Test_timeoutRunner_Run(t *testing.T) {
	t.Parallel()
	opErr := errors.New("the operation failed")

	//: outcome names the single verdict the call must reach, which keeps the
	//: impossible combinations (a timeout that is also a cancellation)
	//: unrepresentable.
	type outcome int
	const (
		succeeds outcome = iota
		timesOut
		propagatesCancellation
		propagatesOpError
	)
	type tc struct {
		name string
		//: the policy's deadline.
		timeout time.Duration
		op      coreres.Operation
		//: cancel the parent before running.
		cancelParent bool
		want         outcome
		//: the operation's own error, for the propagate case.
		opErr error
	}
	tests := []tc{
		{
			name:    "an operation that finishes in time",
			timeout: 5 * time.Second,
			op:      func(context.Context) error { return nil },
		},
		{
			name:    "an operation's own error passes through",
			timeout: 5 * time.Second,
			op:      func(context.Context) error { return opErr },
			want:    propagatesOpError,
			opErr:   opErr,
		},
		{
			//: the defect case: deaf to the deadline, then reports success.
			name:    "an operation that ignores the context and succeeds late",
			timeout: 10 * time.Millisecond,
			op:      func(context.Context) error { time.Sleep(40 * time.Millisecond); return nil },
			want:    timesOut,
		},
		{
			name:    "an operation that honours the context",
			timeout: 10 * time.Millisecond,
			op:      func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
			want:    timesOut,
		},
		{
			//: a parent cancellation is not a deadline.
			name:         "a cancelled parent propagates untouched",
			timeout:      time.Hour,
			op:           func(ctx context.Context) error { return ctx.Err() },
			cancelParent: true,
			want:         propagatesCancellation,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx := t.Context()
		if c.cancelParent {
			stopped, cancel := context.WithCancel(t.Context())
			cancel()
			defer cancel()
			ctx = stopped
		}

		err := NewTimeout(c.timeout).Run(ctx, c.op)

		switch c.want {
		case timesOut:
			if !errors.Is(err, coreres.TimeoutExceeded) {
				t.Fatalf("Run = %v, want TimeoutExceeded", err)
			}
		case propagatesCancellation:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Run = %v, want the cancellation", err)
			}
			//: and it must NOT be dressed up as a timeout.
			if errors.Is(err, coreres.TimeoutExceeded) {
				t.Error("a parent cancellation was relabelled TimeoutExceeded")
			}
		case propagatesOpError:
			if !errors.Is(err, c.opErr) {
				t.Fatalf("Run = %v, want %v verbatim", err, c.opErr)
			}
			if errors.Is(err, coreres.TimeoutExceeded) {
				t.Error("an in-time failure was relabelled TimeoutExceeded")
			}
		case succeeds:
			if err != nil {
				t.Fatalf("Run = %v, want nil", err)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
