package resilience

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// TimeoutConfig parameterises NewTimeoutWith: the deadline and the clock it
// is measured on. Duration has no default and is refused when not positive
// (ADR 0031); a nil Clock is the wall clock.
type TimeoutConfig struct {
	// Duration is how long the Operation may run. A non-positive Duration
	// makes NewTimeoutWith return a policy that refuses every call with
	// PolicyMisconfigured, exactly as NewTimeout refuses a non-positive d.
	Duration time.Duration
	// Clock is what Duration is measured on. Nil means clock.System, under
	// which the policy is NewTimeout's: a context.WithTimeout whose
	// deadline the Operation can read.
	Clock clock.Timed
}
