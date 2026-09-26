// Package lifecycle — the supervisor's construction parameters and the
// defaults its zero values clamp to.
package lifecycle

import (
	"context"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/resilience"
)

// DefaultRestartBase and DefaultRestartMax bound the restart backoff when
// [SupervisorConfig].Backoff is the zero value: one second after the first
// failure, doubling, never more than a minute.
//
// A zero BackoffValue retries at once, which is what a retry around a single
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

// SupervisorConfig tunes [NewSupervisor]. Every field is optional, and the
// zero value supervises on the wall clock, restarting after one second,
// doubling to a minute, and telling nobody. The name and the function are
// NewSupervisor's own arguments, because there is no supervisor without them.
type SupervisorConfig struct {
	// Clock is the time source the supervisor stamps events with and waits
	// its backoff on. Nil means clock.System; a clock.ManualClock drives
	// every restart of a test without a sleep.
	Clock clock.Timed
	// Observe is told about every run started, every run ended, every
	// restart scheduled and the end of the supervision, on the supervisor's
	// goroutine, one call at a time. It must be short and must not panic. Nil
	// observes nothing: the supervisor writes nothing anywhere itself.
	Observe func(SupervisionEventValue)
	// Backoff is the wait before the n-th consecutive restart. The zero
	// value is the [DefaultRestartBase]–[DefaultRestartMax] curve.
	Backoff svcres.BackoffValue
	// HealthyAfter is how long a run must last for its end to reset the
	// consecutive-failure count. Not positive means [DefaultHealthyRun].
	HealthyAfter time.Duration
}

// validateSupervisor refuses a supervisor that could never run.
func validateSupervisor(name string, run func(ctx context.Context) error) error {
	//: an event nobody can attribute is not worth emitting.
	if name == "" {
		//: SupervisorMisconfigured, naming the argument.
		return kerrs.Wrap(SupervisorMisconfigured, kerrs.WrapParams{}, kerrs.String("argument", "name"))
	}
	//: nothing to supervise.
	if run == nil {
		//: SupervisorMisconfigured, naming the argument.
		return kerrs.Wrap(SupervisorMisconfigured, kerrs.WrapParams{},
			kerrs.String("argument", "run"), kerrs.String("supervisor", name))
	}
	//: runnable.
	return nil
}

// resolved applies the documented clamps.
func (c *SupervisorConfig) resolved() (clk clock.Timed, backoff svcres.BackoffValue, healthy time.Duration) {
	clk, backoff, healthy = c.Clock, c.Backoff, c.HealthyAfter
	//: the wall clock is the only non-arbitrary default.
	if clk == nil {
		clk = clock.System
	}
	//: a zero curve would be a hot loop.
	if backoff == (svcres.BackoffValue{}) {
		backoff = svcres.BackoffValue{BaseDelay: DefaultRestartBase, MaxDelay: DefaultRestartMax}
	}
	//: an unset threshold is the default one.
	if healthy <= 0 {
		healthy = DefaultHealthyRun
	}
	//: the values the supervision runs on.
	return clk, backoff, healthy
}
