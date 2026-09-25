// Package resilience — keyed rate-limiter configuration.
package resilience

import (
	"context"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// KeyedRateLimiterConfig parameterises NewKeyedRateLimiter: a token bucket per
// key, at most MaxKeys of them, each forgotten after IdleTimeout without a
// call.
//
// Four fields have no default and a zero in any of them is refused with
// PolicyMisconfigured on every call (ADR 0031): Rate for the reason
// RateLimiterConfig.Rate gives, and the three below because each of their
// zeros has two readings and one of them silently disarms the limiter.
type KeyedRateLimiterConfig struct {
	// Key names who a call is charged to — the authenticated user, the
	// client's address, a tenant. Calls whose keys differ never share a
	// bucket, which is the whole point: one client emptying its bucket is
	// refused while the others are not, so a single client cannot lock
	// everybody out of a sign-in endpoint the way one shared bucket lets it.
	//
	// It is called once per Run, with the Run's context. It is required: a
	// limiter with nothing to key on is NewRateLimiter.
	Key func(ctx context.Context) string
	// Clock reads time for every bucket and for idleness. Nil means
	// clock.System.
	Clock clock.Clock
	// Rate is each key's sustained admission rate, in tokens per second. It
	// has no default, exactly as RateLimiterConfig.Rate has none.
	Rate float64
	// Burst is each key's bucket capacity, clamped to at least 1.
	Burst int
	// MaxKeys bounds how many keys are tracked at once; beyond it the least
	// recently used key is forgotten. It is required: its zero reads either
	// as "unbounded", which lets a stream of distinct keys grow the process
	// without limit, or as "none", which admits nothing. Size it above the
	// number of clients active within IdleTimeout — a key pushed out by
	// capacity comes back with a FULL bucket, so a flood of new keys buys
	// every evicted client a fresh burst.
	MaxKeys int
	// IdleTimeout is how long a key may go without a call before it is
	// forgotten; its next call starts over with a full bucket. It slides: a
	// key in use is never forgotten for idleness. It is required: its zero
	// reads either as "forget at once", which gives every call a full bucket
	// and so limits nothing, or as "never".
	//
	// Forgetting a key is observable only when IdleTimeout is shorter than the
	// time the bucket takes to refill (Burst / Rate): below that, a key that
	// comes back is admitted a burst it would not yet have earned.
	IdleTimeout time.Duration
}
