package resilience

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// AdaptiveThrottleConfig parameterises NewAdaptiveThrottle, in the terms
// of Google's SRE book: the multiplier K, the window the requests and
// accepts are counted over, and what counts as a refusal. Every field
// has a default; a value out of range is refused with
// PolicyMisconfigured, naming the field (ADR 0031).
type AdaptiveThrottleConfig struct {
	// K is how many requests per accept the throttle lets through before
	// it starts refusing: a call is refused with probability
	// max(0, (requests − K × accepts) / (requests + 1)). Zero is 2, the
	// SRE book's value. A lower K refuses sooner and wastes less of the
	// dependency's capacity on calls it will refuse; a higher one refuses
	// later and keeps more traffic flowing. A K below 1 would refuse calls
	// to a dependency accepting every one of them, and is refused, as is
	// one that is not a finite number.
	K float64
	// Window is how far back the requests and accepts are counted. Zero
	// is two minutes, the SRE book's history. A negative Window is
	// refused.
	Window time.Duration
	// Buckets is how many slots the window is kept in: a slot leaves the
	// window whole, so it reaches back between Window − Window/Buckets
	// and Window. Zero is 10; more than 1000 is refused, as is a Window
	// too short to give each slot a nanosecond.
	Buckets int
	// Rejected classifies a non-nil error of the operation: true is a
	// refusal by the dependency — overload, quota, unavailability —,
	// false an answer it gave, which counts as an accept however
	// unwelcome. Nil counts every error as a refusal. It is not
	// consulted for a context.Canceled returned once the caller's
	// context is closed: that call counts as an accept.
	Rejected func(error) bool
	// Clock is what the window is kept on. Nil means clock.System.
	Clock clock.Clock
	// Random returns a number in [0, 1) each time a call might be
	// refused; the call is refused when it is below the probability.
	// Nil is math/rand/v2's Float64. A test hands it a sequence to
	// decide each refusal.
	Random func() float64
}
