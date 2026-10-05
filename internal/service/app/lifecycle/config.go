package lifecycle

import (
	"time"

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
