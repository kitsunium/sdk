// Package resilience — retry-with-exponential-backoff policy.
package resilience

import (
	"context"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
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
	//: a sub-1 multiplier would shrink the delay, and a NaN one would poison
	//: every product — both default to doubling, through the same
	//: normalisation the public BackoffValue applies.
	cfg.Multiplier = normalMultiplier(cfg.Multiplier)
	//: NaN FIRST, inside normalJitter: Go's min/max propagate it, so
	//: min(max(NaN, 0), 1) is NaN, and NaN <= 0 is false — it would sail past
	//: every later guard into a float-to-int conversion Go leaves
	//: implementation-defined. A jitter wider than the delay is a different
	//: policy, so the rest is clamped into [0, 1].
	cfg.Jitter = normalJitter(cfg.Jitter)
	//: resolved once, so every wait reads the same clock.
	cfg.Clock = timedOrSystem(cfg.Clock)
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

// wait sleeps the backoff for the given attempt on the configured clock,
// returning ctx.Err on cancel.
func (r *retryRunner) wait(ctx context.Context, attempt int) error {
	//: a deadline-aware sleep on the injected clock, so a test drives the
	//: whole backoff with a ManualClock instead of sleeping through it.
	timer := timedOrSystem(r.cfg.Clock).NewTimer(r.jittered(r.backoff(attempt)))
	//: always release the timer.
	defer timer.Stop()
	//: race the backoff timer against context cancellation.
	select {
	case <-ctx.Done():
		//: cancelled before the delay elapsed.
		return ctx.Err()
	case <-timer.C():
		//: the backoff elapsed normally.
		return nil
	}
}

// backoff computes the capped exponential delay for attempt (1-based growth).
// It is the public BackoffValue's curve, built from this policy's four fields, so
// the two cannot drift apart.
func (r *retryRunner) backoff(attempt int) time.Duration {
	//: the multiplier NewRetry already normalised is normalised again here,
	//: which is idempotent and covers a runner built without NewRetry.
	return grow(r.cfg.BaseDelay, r.cfg.MaxDelay, normalMultiplier(r.cfg.Multiplier), attempt)
}

// jittered widens delay by a uniform random fraction of itself, or returns it
// unchanged when no jitter is configured.
//
// It is separate from backoff so backoff stays a PURE function of the attempt
// number: the deterministic growth and the randomisation are two different
// claims, and a suite that cannot assert the first without the second can
// assert neither precisely.
func (r *retryRunner) jittered(delay time.Duration) time.Duration {
	//: NewRetry normalises NaN and clamps the range; normalJitter repeats it
	//: for a runner built without NewRetry.
	return widen(delay, normalJitter(r.cfg.Jitter))
}

// timedOrSystem resolves a nil clock to the wall clock.
func timedOrSystem(clk clock.Timed) clock.Timed {
	//: nil is the caller with no opinion, which is the production case.
	if clk == nil {
		//: the wall clock.
		return clock.System
	}
	//: the caller's own, usually a ManualClock in a test.
	return clk
}
