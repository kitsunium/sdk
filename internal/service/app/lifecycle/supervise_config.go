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
	// value is the [DefaultRestartBase]–[DefaultRestartMax] curve, and a curve
	// without a positive BaseDelay starts from [DefaultRestartBase] under its
	// own ceiling: a supervisor never restarts at once.
	Backoff kbackoff.Value
	// HealthyAfter is how long a run must last for its end to reset the
	// consecutive-failure count. Not positive means [DefaultHealthyRun].
	HealthyAfter time.Duration
	// GiveUpAfter bounds how often the function may end early before the
	// supervisor stops restarting it. The zero value restarts it forever,
	// as a supervisor always has.
	GiveUpAfter GiveUpValue
}

// GiveUpValue is a restart intensity, Erlang's: a supervisor gives up when
// its function ends early more than Restarts times within any span of
// Within on the supervisor's clock. The (Restarts+1)-th early end inside
// Within is not restarted: the supervision ends, its observer hears
// SupervisionGaveUp, and Supervisor.Check reports SupervisorGaveUp, so
// the level above — a liveness probe, an orchestrator — decides what
// happens next instead of a loop that cannot succeed restarting forever.
//
// The zero value never gives up. Restarts zero with a positive Within
// gives up at the first early end. A negative field, and a positive
// Restarts without a positive Within, are refused by NewSupervisor with
// SupervisorMisconfigured: a bound with no span is no bound.
type GiveUpValue struct {
	// Restarts is how many early ends Within tolerates, each restarted.
	Restarts int
	// Within is the span the early ends are counted over.
	Within time.Duration
}

// enabled reports whether the bound gives up at all: its zero value never does.
func (g GiveUpValue) enabled() bool {
	//: a span to count over is what turns the bound on.
	return g.Within > 0
}

// validateGiveUp refuses a restart intensity that is no bound: a negative
// field, or restarts counted over no span.
func validateGiveUp(name string, giveUp GiveUpValue) error {
	//: the field the refusal names, empty when the bound is usable.
	field := ""
	//: the first field that cannot be honoured.
	switch {
	case giveUp.Restarts < 0:
		field = "GiveUpAfter.Restarts"
	case giveUp.Within < 0, giveUp.Restarts > 0 && giveUp.Within == 0:
		field = "GiveUpAfter.Within"
	}
	//: usable, or the zero value.
	if field == "" {
		//: accepted.
		return nil
	}
	//: SupervisorMisconfigured, naming the field.
	return kerrs.Wrap(corelc.SupervisorMisconfigured, kerrs.WrapParams{},
		kerrs.String("argument", field), kerrs.String("supervisor", name))
}

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
