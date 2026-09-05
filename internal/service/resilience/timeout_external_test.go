// Package resilience_test — the timeout policy as a caller configures it.
package resilience_test

import (
	"context"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/resilience"
)

// TestNewTimeout pins the refusal of a non-positive duration, and why it is a
// refusal rather than a default.
//
// A deadline IS this policy — there is no SDK-side duration that is not a guess
// at the caller's requirement. And context.WithTimeout(ctx, 0) yields an
// ALREADY-EXPIRED context, so honouring a zero would fail even an instantaneous
// operation with TIMEOUT_EXCEEDED: a policy that can never admit anything,
// reporting it with the error it uses when it is working.
func TestNewTimeout(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		d    time.Duration
		//: whether every call must be refused as a misconfiguration.
		wantMisconfigured bool
	}
	tests := []tc{
		{name: "a generous deadline", d: 5 * time.Second},
		{name: "a short deadline", d: 50 * time.Millisecond},
		{name: "a zero duration is refused", d: 0, wantMisconfigured: true},
		{name: "a negative duration is refused", d: -time.Second, wantMisconfigured: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := svcres.NewTimeout(c.d)

		ran := false
		err := r.Run(t.Context(), func(context.Context) error {
			ran = true
			return nil
		})

		if c.wantMisconfigured {
			//: the refusal says the policy is misconfigured rather than that
			//: the operation was too slow — which it never got the chance to be.
			if !kerrs.HasCode(err, coreres.CodePolicyMisconfigured) {
				t.Fatalf("Run = %v, want POLICY_MISCONFIGURED", err)
			}
			if ran {
				t.Error("a misconfigured timeout still ran the operation")
			}
			return
		}
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		if !ran {
			t.Error("the operation did not run")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestTimeoutRunsUnderDeadline pins the two directions: an operation that
// finishes in time succeeds, and one that overruns is reported typed.
//
// The late-success case is the subtle one. An operation that IGNORES its context
// and returns nil after the deadline has passed must still fail: relabelling it
// as success silently voids the policy, which is the difference between
// degrading to op granularity — what the doc promises — and abandoning the
// deadline altogether.
func TestTimeoutRunsUnderDeadline(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		d    time.Duration
		//: how long the operation takes, and whether it honours ctx.
		work        time.Duration
		honoursCtx  bool
		wantTimeout bool
	}
	tests := []tc{
		{name: "a fast operation", d: 5 * time.Second, work: 0, honoursCtx: true},
		{
			name:        "a slow operation that honours the context",
			d:           50 * time.Millisecond,
			work:        5 * time.Second,
			honoursCtx:  true,
			wantTimeout: true,
		},
		{
			//: ignores ctx and reports success LATE — still a timeout.
			name:        "a slow operation that ignores the context",
			d:           20 * time.Millisecond,
			work:        100 * time.Millisecond,
			wantTimeout: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := svcres.NewTimeout(c.d)

		err := r.Run(t.Context(), func(ctx context.Context) error {
			if !c.honoursCtx {
				//: deliberately deaf to the deadline.
				time.Sleep(c.work)
				return nil
			}
			select {
			case <-time.After(c.work):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})

		if c.wantTimeout {
			if !kerrs.HasCode(err, coreres.CodeTimeoutExceeded) {
				t.Fatalf("Run = %v, want TIMEOUT_EXCEEDED", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
