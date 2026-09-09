// Package lifecycle — hosts Config, the engine's construction parameters.
package lifecycle

import (
	"time"

	corelc "github.com/kitsunium/sdk/internal/core/lifecycle"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// DefaultStopTimeout is the per-component shutdown budget a non-positive
// [Config.StopTimeout] clamps to.
//
// The number is exported because a default nobody can name is a default
// nobody can reason about: a caller sizing a container's termination grace
// period needs it, and a test asserting the clamp needs to say what it
// clamped TO without copying a literal. Thirty seconds is the conventional
// floor of the surrounding ecosystem — Kubernetes' terminationGracePeriod
// defaults to 30s — so it is a value a reader accepts without being told why,
// which is exactly ADR 0031's test for clamping rather than refusing.
const DefaultStopTimeout time.Duration = 30 * time.Second

// Config parameterises [New]. Every field is optional and the zero value
// builds a working Lifecycle — one that runs on the wall clock, gives each
// component [DefaultStopTimeout] to stop, and reports nothing.
type Config struct {
	// Clock is the time source the engine reads AND waits on. A nil Clock
	// falls back to clock.System.
	//
	// It is clock.Timed rather than clock.Clock because this engine does both
	// halves: it stamps transitions and it waits out shutdown budgets.
	// Depending on package time directly instead would make every budget
	// assertion in the test suite a sleep, and a sleep a tolerance, and a
	// tolerance a flake somebody eventually deletes.
	Clock clock.Timed
	// StopTimeout is the budget ONE component's Stop gets. It is deliberately
	// per-component and not a budget for the whole shutdown: a shared budget
	// lets the first component that will not finish consume the budget of
	// every component after it, which is precisely the defect ADR 0043
	// removed from the HTTP drain.
	//
	// The price is stated rather than hidden: a Stop of n components returns
	// in at most n × StopTimeout. That is the cost of not letting one
	// component spend everyone else's budget.
	//
	// A non-positive value CLAMPS to [DefaultStopTimeout]. It is never read
	// as "stop immediately" — a zero here is what an unset field looks like,
	// and reading an unset field as "give every component no time at all"
	// would turn a forgotten line into a shutdown that reports a timeout for
	// components that were about to succeed (ADR 0031).
	StopTimeout time.Duration
	// OnTransition observes every call the engine makes into a component —
	// each Start, each Stop, each Stop abandoned at its budget.
	//
	// Calls are SERIALISED: the hook need not be safe for concurrent use, and
	// in exchange a hook that blocks blocks the lifecycle. Keep it short; hand
	// off to a logger or a channel rather than doing work in it.
	//
	// A nil hook is a working configuration, not a refusal. It does mean the
	// transitions go NOWHERE — the SDK will not write them to stderr on the
	// caller's behalf (ADR 0030) — so a caller who wants a startup log must
	// wire this. The errors are still returned from Start and Stop either way.
	OnTransition func(corelc.TransitionValue)
}

// stopBudget resolves the configured budget, applying the documented clamp.
func (c Config) stopBudget() time.Duration {
	//: a non-positive budget is an unset field, not a request for zero time.
	if c.StopTimeout <= 0 {
		//: the documented floor.
		return DefaultStopTimeout
	}
	//: the caller's own budget.
	return c.StopTimeout
}

// resolvedClock resolves the time source, applying the documented fallback.
func (c Config) resolvedClock() clock.Timed {
	//: the wall clock is the only non-arbitrary default here.
	if c.Clock == nil {
		//: never mutate clock.System at package scope.
		return clock.System
	}
	//: the caller's injected clock.
	return c.Clock
}
