package resilience

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// RateLimiterConfig parameterises NewRateLimiter. Burst is the bucket capacity
// (clamped to >= 1); a nil Clock defaults to System; a zero MaxWait
// rejects at once a call that finds no token.
type RateLimiterConfig struct {
	// Rate is the sustained admission rate in tokens per second. It has no
	// default: a Rate that is not a finite positive number — zero, negative,
	// NaN or infinite — makes NewRateLimiter return a policy that refuses
	// every call with PolicyMisconfigured. Any rate the SDK picked on
	// the caller's behalf would be a guess at their requirement, and a caller
	// who silently received one would believe they were limited at the rate
	// they intended (ADR 0031). Contrast Burst, which does have an obvious
	// floor — admit at least one call — and is therefore clamped.
	Rate  float64
	Burst int
	Clock clock.Clock
	// MaxWait is how long a call may wait for its token instead of being
	// rejected: a call whose token is due within MaxWait reserves it at
	// once — later calls queue behind the reservation — and waits for
	// it on Clock. Zero, or less, rejects at once with RateLimited, as
	// the limiter always has. A call whose token is due later than
	// MaxWait is rejected at once, saying when it would have been due.
	//
	// Waiting needs a clock that can wait: with a positive MaxWait, a
	// Clock that is not a clock.Timed is refused with
	// PolicyMisconfigured, naming Clock. A nil Clock waits on the wall
	// clock.
	MaxWait time.Duration
}
