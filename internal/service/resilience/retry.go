// Package resilience — retry-with-exponential-backoff policy.
package resilience

import (
	"context"
	"math/rand/v2"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
)

const (
	// defaultMultiplier is the backoff growth factor when none is configured.
	defaultMultiplier float64 = 2
	// minAttempts is the floor on the attempt budget.
	minAttempts int = 1
	// maxJitter is the ceiling on RetryConfig.Jitter. Above 1 the random
	// component would exceed the delay it widens, which is a different policy
	// (randomised wait) wearing this one's name.
	maxJitter float64 = 1
	// noJitter is the deterministic backoff, and the zero value.
	noJitter float64 = 0
)

// retryRunner retries op with capped exponential backoff between attempts.
type retryRunner struct {
	cfg RetryConfig
}

// NewRetry returns a Runner that retries op up to cfg.MaxAttempts times,
// sleeping an exponentially-growing (capped) delay between attempts and aborting
// early on ctx cancellation. The final attempt's error wraps as RetryExhausted.
// An error rejected by cfg.Retryable ends the loop at once and is returned
// verbatim, so a deterministic failure neither spends the budget nor hides
// behind RetryExhausted.
func NewRetry(cfg RetryConfig) coreres.Runner {
	//: clamp the attempt budget to at least one.
	if cfg.MaxAttempts < minAttempts {
		//: a non-positive budget still runs once.
		cfg.MaxAttempts = minAttempts
	}
	//: a sub-1 multiplier would shrink the delay — default to doubling.
	if cfg.Multiplier < defaultMultiplier && cfg.Multiplier <= 1 {
		//: standard exponential doubling.
		cfg.Multiplier = defaultMultiplier
	}
	//: a jitter wider than the delay is a different policy; clamp into [0, 1].
	cfg.Jitter = min(max(cfg.Jitter, noJitter), maxJitter)
	//: stateless config holder — safe to share.
	return &retryRunner{cfg: cfg}
}

// Run executes op, retrying on transient errors until the budget is spent. An
// error the classifier rejects is returned as-is on the spot.
func (r *retryRunner) Run(ctx context.Context, op coreres.Operation) error {
	//: track the last error so exhaustion can wrap it.
	var lastErr error
	//: each attempt (attempt 0 runs immediately; later ones back off first).
	for attempt := range r.cfg.MaxAttempts {
		//: a non-first attempt waits the backoff (or aborts on ctx).
		if attempt > 0 {
			//: a cancelled wait propagates ctx.Err immediately.
			if waitErr := r.wait(ctx, attempt); waitErr != nil {
				//: ctx cancelled mid-backoff.
				return waitErr
			}
		}
		//: run the guarded operation.
		lastErr = op(ctx)
		//: a success ends the retry loop.
		if lastErr == nil {
			//: done.
			return nil
		}
		//: a cancelled context stops further retries.
		if ctx.Err() != nil {
			//: surface the cancellation, not RetryExhausted.
			return ctx.Err()
		}
		//: a deterministic error is not worth replaying — hand it back untouched.
		if !isRetryable(r.cfg.Retryable, lastErr) {
			//: verbatim: no backoff, no budget spent, no RetryExhausted relabel.
			return lastErr
		}
	}
	//: budget exhausted — relabel the last error as RetryExhausted.
	return wrapAs(coreres.RetryExhausted, lastErr)
}

// wait sleeps the backoff for the given attempt, returning ctx.Err on cancel.
func (r *retryRunner) wait(ctx context.Context, attempt int) error {
	//: a deadline-aware sleep: whichever fires first wins.
	timer := time.NewTimer(r.jittered(r.backoff(attempt)))
	//: always release the timer.
	defer timer.Stop()
	//: race the backoff timer against context cancellation.
	select {
	case <-ctx.Done():
		//: cancelled before the delay elapsed.
		return ctx.Err()
	case <-timer.C:
		//: the backoff elapsed normally.
		return nil
	}
}

// backoff computes the capped exponential delay for attempt (1-based growth).
func (r *retryRunner) backoff(attempt int) time.Duration {
	//: start from the base delay as a float for the geometric growth.
	delay := float64(r.cfg.BaseDelay)
	//: multiply once per prior attempt.
	for range attempt - 1 {
		//: geometric growth by the configured multiplier.
		delay *= r.cfg.Multiplier
	}
	//: apply the optional ceiling.
	if r.cfg.MaxDelay > 0 && time.Duration(delay) > r.cfg.MaxDelay {
		//: clamp to the configured maximum.
		return r.cfg.MaxDelay
	}
	//: the grown (uncapped) delay.
	return time.Duration(delay)
}

// jittered widens delay by a uniform random fraction of itself, or returns it
// unchanged when no jitter is configured.
//
// It is separate from backoff so backoff stays a PURE function of the attempt
// number: the deterministic growth and the randomisation are two different
// claims, and a suite that cannot assert the first without the second can
// assert neither precisely.
func (r *retryRunner) jittered(delay time.Duration) time.Duration {
	//: the zero value is the deterministic backoff this policy always had.
	if r.cfg.Jitter <= noJitter {
		//: unchanged.
		return delay
	}
	width := int64(float64(delay) * r.cfg.Jitter)
	//: rand.Int64N panics on a non-positive bound, which a sub-nanosecond
	//: delay or a rounded-to-zero width reaches. Nothing to spread there.
	if width <= 0 {
		//: unchanged.
		return delay
	}

	//: uniform in [delay, delay+width) — additive, so the average wait keeps
	//: growing with the attempt rather than being pulled back toward zero.
	return delay + time.Duration(rand.Int64N(width))
}
