package lifecycle

import (
	"context"
	"time"

	corelc "github.com/kitsunium/sdk/internal/core/app/lifecycle"
	kbackoff "github.com/kitsunium/sdk/internal/kernel/backoff"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// DefaultRestartBase and DefaultRestartMax bound the restart backoff when
// [SupervisorConfig].Backoff is the zero value: one second after the first
// failure, doubling, never more than a minute.
//
// A zero backoff.Value retries at once, which is what a retry around a single
// call means by it — and for a supervised loop it is a hot loop: a function
// that fails on entry would be restarted as fast as a core can run it. The
// zero is therefore CLAMPED to a curve a reader accepts without being told
// why (ADR 0031): fast enough that a transient failure costs a second, slow
// enough that a permanent one costs a log line a minute.
const (
	DefaultRestartBase time.Duration = time.Second
	DefaultRestartMax  time.Duration = time.Minute
)

// DefaultHealthyRun is how long a run must last, when
// [SupervisorConfig].HealthyAfter is not positive, for its end to count as a
// first failure again rather than one more in a row. A run that lasted a
// minute was working, and punishing its next failure with the backoff of a
// loop that has been crashing on entry would slow a recovery for nothing.
const DefaultHealthyRun time.Duration = time.Minute

// validateSupervisor refuses a supervisor that could never run.
func validateSupervisor(name string, run func(ctx context.Context) error) error {
	//: an event nobody can attribute is not worth emitting.
	if name == "" {
		//: SupervisorMisconfigured, naming the argument.
		return kerrs.Wrap(corelc.SupervisorMisconfigured, kerrs.WrapParams{}, kerrs.String("argument", "name"))
	}
	//: nothing to supervise.
	if run == nil {
		//: SupervisorMisconfigured, naming the argument.
		return kerrs.Wrap(corelc.SupervisorMisconfigured, kerrs.WrapParams{},
			kerrs.String("argument", "run"), kerrs.String("supervisor", name))
	}
	//: runnable.
	return nil
}

// resolved applies the documented clamps.
func (c *SupervisorConfig) resolved() (clk clock.Timed, backoff kbackoff.Value, healthy time.Duration) {
	clk, backoff, healthy = c.Clock, c.Backoff, c.HealthyAfter
	//: the wall clock is the only non-arbitrary default.
	if clk == nil {
		clk = clock.System
	}
	//: a zero curve would be a hot loop.
	if backoff == (kbackoff.Value{}) {
		backoff = kbackoff.Value{BaseDelay: DefaultRestartBase, MaxDelay: DefaultRestartMax}
	}
	//: so would a curve without a base, whatever else it sets: it restarts at
	//: once. The caller's ceiling, factor and jitter are kept.
	if backoff.BaseDelay <= 0 {
		backoff.BaseDelay = DefaultRestartBase
	}
	//: an unset threshold is the default one.
	if healthy <= 0 {
		healthy = DefaultHealthyRun
	}
	//: the values the supervision runs on.
	return clk, backoff, healthy
}
