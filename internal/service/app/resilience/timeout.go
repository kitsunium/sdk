package resilience

import (
	"context"
	"errors"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// timeoutRunner caps an Operation's run time with a deadline. A nil clk is the
// wall clock, through context.WithTimeout; any other clock goes through
// clockDeadline.
type timeoutRunner struct {
	timeout time.Duration
	clk     clock.Timed
}

// NewTimeout returns a Runner that fails an Operation with TimeoutExceeded if it
// does not finish within d. The Operation MUST honour ctx for the deadline to
// fire promptly (a ctx-ignoring op caps precision at op granularity).
//
// A non-positive d is refused: every call returns PolicyMisconfigured without
// running the operation (ADR 0031). context.WithTimeout(ctx, 0) yields an
// already-expired context, so the previous behaviour failed even an
// instantaneous operation with TimeoutExceeded — a policy that could never
// admit anything, reporting it with the error it uses when it is working.
//
// Only this policy's own deadline is reported as TimeoutExceeded. When the
// caller's context is already finished — cancelled, or past a deadline of
// its own — the call returns that context's error instead: the caller
// left or ran out of time, and the policy did not time anything out.
//
// The deadline runs on the wall clock; NewTimeoutWith takes a clock.
func NewTimeout(d time.Duration) coreres.Runner {
	//: the wall-clock form of the one constructor.
	return NewTimeoutWith(TimeoutConfig{Duration: d})
}

// NewTimeoutWith is NewTimeout with the deadline measured on cfg.Clock, so
// a test expires it by advancing a clock.ManualClock past cfg.Duration
// instead of sleeping through it (ADR 0090). A nil cfg.Clock, or
// clock.System, is NewTimeout(cfg.Duration) exactly.
//
// Under any other clock the Operation's context reports
// context.DeadlineExceeded once the clock passes the deadline, and its
// Deadline method reports the caller's deadline, not this one: a deadline
// on a clock that is not the wall clock is not a time a socket can be
// given. A non-positive cfg.Duration is refused like NewTimeout's.
func NewTimeoutWith(cfg TimeoutConfig) coreres.Runner {
	//: a deadline IS this policy — there is no SDK-side duration that is not a
	//: guess at the caller's requirement, so refuse rather than invent one.
	if cfg.Duration <= 0 {
		//: fail closed, and say why.
		return newMisconfigured("timeout", "duration")
	}
	//: the wall clock keeps context.WithTimeout, whose deadline a socket can be
	//: given; nil stands for it so Run decides on one comparison.
	clk := cfg.Clock
	//: the system clock is the wall clock under another name.
	if clk == clock.System {
		//: same path as nil.
		clk = nil
	}
	//: a stateless value runner — safe to share.
	return timeoutRunner{timeout: cfg.Duration, clk: clk}
}

// Run executes op under a derived deadline.
func (t timeoutRunner) Run(ctx context.Context, op coreres.Operation) error {
	//: derive a deadline-bound context; cancel releases its resources.
	dctx, cancel := t.derive(ctx)
	defer cancel()
	//: run the operation against the deadline-bound context.
	err := op(dctx)
	//: the deadline is authoritative regardless of what op returned. An op that
	//: ignores ctx and reports success AFTER the deadline expired must not be
	//: relabelled as success — that silently voids the policy, which is exactly
	//: the "op granularity" precision the doc promises to degrade to, not to
	//: abandon. Checking dctx.Err() unconditionally (rather than only when
	//: err != nil) is what makes the late-success case fail honestly.
	if dctx.Err() == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) {
		//: the caller's own context finished first — it was cancelled, or its
		//: own deadline passed: that is the caller's outcome, not this
		//: policy's, and a TimeoutExceeded (HTTP 504) would blame a dependency
		//: for a client that went away.
		if parentErr := ctx.Err(); parentErr != nil {
			//: the caller's own error, as context reports it.
			return parentErr
		}
		//: relabel as TIMEOUT_EXCEEDED, keeping the cause in the trail.
		return wrapAs(coreres.TimeoutExceeded, err)
	}
	//: otherwise propagate the operation's own outcome.
	return err
}

// derive returns the Operation's deadline-bound context on the runner's clock.
func (t timeoutRunner) derive(ctx context.Context) (context.Context, context.CancelFunc) {
	//: the wall clock: the standard library's timer and a readable deadline.
	if t.clk == nil {
		//: exactly the historical behaviour.
		return context.WithTimeout(ctx, t.timeout)
	}
	//: any other clock: a context that expires when that clock says so.
	return newClockDeadline(ctx, t.clk, t.timeout)
}
