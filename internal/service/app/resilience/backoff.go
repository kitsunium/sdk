// Package resilience — the exponential backoff every waiting policy computes,
// re-exported from the kernel package that owns it.
package resilience

import kbackoff "github.com/kitsunium/sdk/internal/kernel/backoff"

// BackoffValue is an exponential backoff: the wait after the n-th consecutive
// failure is BaseDelay × Multiplier^(n−1), held at MaxDelay when one is set and
// widened by Jitter when one is asked for. Delay(attempt) never returns a
// negative duration, however large attempt is (ADR 0103).
//
// It is the policy NewRetry has always applied between attempts, published so
// a loop that retries on its own terms — a supervised goroutine, an outbox, a
// state machine's failed transition — computes the same curve instead of
// writing a fourth copy of it. RetryConfig keeps its four fields of the same
// names; NewRetry builds its waits from the same two halves.
//
// It is an alias of kernel/backoff.Value, the layer that OWNS the curve (ADR
// 0074): the curve names no policy and no domain, so the domains that only
// need the curve import the kernel directly instead of this package.
type BackoffValue = kbackoff.Value
