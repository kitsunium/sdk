// Package resilience_test — the timeout policy on an injected clock, and the
// caller's context finishing first.
package resilience_test

import (
	"context"
	"errors"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/app/resilience"
)

// TestTimeout_TheCallersContextIsNotRelabelled pins #477's timeout half: only
// the policy's own deadline is TIMEOUT_EXCEEDED. A caller whose context ended
// first — cancelled, or past a deadline of its own — gets that context's error,
// so a client that went away is not reported as a 504 from the dependency.
func TestTimeout_TheCallersContextIsNotRelabelled(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		parent func(t *testing.T) context.Context
		//: what the operation returns.
		result func(ctx context.Context) error
		want   error
	}
	pastDeadline := func(t *testing.T) context.Context {
		t.Helper()
		ctx, cancel := context.WithDeadline(t.Context(), time.Unix(0, 0))
		t.Cleanup(cancel)
		return ctx
	}
	tests := []tc{
		{
			name: "the caller's own deadline passed", parent: pastDeadline,
			result: func(ctx context.Context) error { return ctx.Err() }, want: context.DeadlineExceeded,
		},
		{
			name: "the caller cancelled, the operation saw a deadline", parent: cancelledContext,
			result: func(context.Context) error { return context.DeadlineExceeded }, want: context.Canceled,
		},
		{
			name: "the caller cancelled, the operation reported it", parent: cancelledContext,
			result: func(ctx context.Context) error { return ctx.Err() }, want: context.Canceled,
		},
	}
	newRunners := map[string]func() coreres.Runner{
		"NewTimeout": func() coreres.Runner { return svcres.NewTimeout(time.Hour) },
		"NewTimeoutWith on a manual clock": func() coreres.Runner {
			return svcres.NewTimeoutWith(svcres.TimeoutConfig{
				Duration: time.Hour, Clock: clock.NewManualClock(time.Unix(1_700_000_000, 0)),
			})
		},
	}
	for runnerName, newRunner := range newRunners {
		for _, c := range tests {
			t.Run(runnerName+"/"+c.name, func(t *testing.T) {
				t.Parallel()
				err := newRunner().Run(c.parent(t), c.result)
				if kerrs.HasCode(err, coreres.CodeTimeoutExceeded) {
					t.Fatalf("Run = %v: the caller's own context was relabelled TIMEOUT_EXCEEDED", err)
				}
				if !errors.Is(err, c.want) {
					t.Fatalf("Run = %v, want %v", err, c.want)
				}
			})
		}
	}
}

// TestTimeoutWithWaitsOnItsClock pins NewTimeoutWith: the deadline is the
// manual clock's, it expires when that clock passes it and not a nanosecond
// before, the Operation's context reports DeadlineExceeded — and so does a
// context the Operation derives from it — and the outcome is TIMEOUT_EXCEEDED.
//
// GOROUTINE LIFECYCLE: one goroutine runs the policy; the test drives its
// deadline by advancing the clock and reads its result before returning.
func TestTimeoutWithWaitsOnItsClock(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	r := svcres.NewTimeoutWith(svcres.TimeoutConfig{Duration: time.Second, Clock: manual})
	seen := make(chan error, 2)
	done := make(chan error, 1)
	go func() {
		done <- r.Run(t.Context(), func(ctx context.Context) error {
			child, cancel := context.WithCancel(ctx)
			defer cancel()
			<-child.Done()
			seen <- ctx.Err()
			seen <- child.Err()
			return ctx.Err()
		})
	}()

	//: the deadline's timer is armed on the manual clock.
	manual.BlockUntil(1)
	manual.Advance(time.Second - time.Nanosecond)
	if got := manual.Pending(); got != 1 {
		t.Fatalf("%d timers pending a nanosecond before the deadline, want the deadline's", got)
	}
	select {
	case err := <-done:
		t.Fatalf("Run returned %v before the deadline", err)
	default:
	}
	manual.Advance(time.Nanosecond)

	err := <-done
	if !kerrs.HasCode(err, coreres.CodeTimeoutExceeded) {
		t.Fatalf("Run = %v, want TIMEOUT_EXCEEDED", err)
	}
	if got := <-seen; got != context.DeadlineExceeded {
		t.Errorf("the Operation's ctx.Err() = %v, want context.DeadlineExceeded", got)
	}
	if got := <-seen; got != context.DeadlineExceeded {
		t.Errorf("a derived context's Err() = %v, want context.DeadlineExceeded", got)
	}
}

// TestTimeoutWith_ALateSuccessIsStillATimeout pins the deadline as
// authoritative on an injected clock as on the wall one: an Operation deaf to
// its context, returning nil after the clock passed the deadline, is a timeout.
func TestTimeoutWith_ALateSuccessIsStillATimeout(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	r := svcres.NewTimeoutWith(svcres.TimeoutConfig{Duration: time.Second, Clock: manual})
	err := r.Run(t.Context(), func(ctx context.Context) error {
		manual.Advance(time.Second)
		//: the expiry is observed before the deaf success is reported.
		<-ctx.Done()
		return nil
	})
	if !kerrs.HasCode(err, coreres.CodeTimeoutExceeded) {
		t.Fatalf("Run = %v, want TIMEOUT_EXCEEDED", err)
	}
}

// TestTimeoutWith_TheContextItHands pins what the Operation's context is under
// each clock: the wall clock keeps context.WithTimeout's readable deadline; an
// injected one reports the caller's deadline, carries the caller's values, and
// is live until something ends it.
func TestTimeoutWith_TheContextItHands(t *testing.T) {
	t.Parallel()
	type key struct{}
	type tc struct {
		name         string
		clk          clock.Timed
		wantDeadline bool
	}
	tests := []tc{
		{name: "a nil clock is the wall clock", clk: nil, wantDeadline: true},
		{name: "clock.System is the wall clock", clk: clock.System, wantDeadline: true},
		{name: "a manual clock", clk: clock.NewManualClock(time.Unix(1_700_000_000, 0)), wantDeadline: false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			parent := context.WithValue(t.Context(), key{}, "carried")
			r := svcres.NewTimeoutWith(svcres.TimeoutConfig{Duration: time.Hour, Clock: c.clk})
			err := r.Run(parent, func(ctx context.Context) error {
				if _, ok := ctx.Deadline(); ok != c.wantDeadline {
					t.Errorf("Deadline() ok = %v, want %v", ok, c.wantDeadline)
				}
				if got := ctx.Value(key{}); got != "carried" {
					t.Errorf("Value = %v, want the caller's value", got)
				}
				if ctx.Err() != nil {
					t.Errorf("Err() = %v on a live context", ctx.Err())
				}
				return nil
			})
			if err != nil {
				t.Fatalf("Run = %v, want nil", err)
			}
		})
	}
}

// TestNewTimeoutWith_RefusesANonPositiveDuration pins the refusal NewTimeout
// already makes, through the configuration form.
func TestNewTimeoutWith_RefusesANonPositiveDuration(t *testing.T) {
	t.Parallel()
	for _, d := range []time.Duration{0, -time.Second} {
		ran := false
		err := svcres.NewTimeoutWith(svcres.TimeoutConfig{
			Duration: d, Clock: clock.NewManualClock(time.Unix(0, 0)),
		}).Run(t.Context(), func(context.Context) error {
			ran = true
			return nil
		})
		if ran || !kerrs.HasCode(err, coreres.CodePolicyMisconfigured) {
			t.Fatalf("Duration %v: ran=%v err=%v, want POLICY_MISCONFIGURED without running", d, ran, err)
		}
	}
}
