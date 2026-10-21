package resilience

import (
	"context"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	kbackoff "github.com/kitsunium/sdk/internal/kernel/backoff"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// minAttempts is the floor on the attempt budget.
const minAttempts int = 1

// retryRunner retries op with capped exponential backoff between attempts.
type retryRunner struct {
	cfg RetryConfig
}

// NewRetry returns a Runner that retries op up to cfg.MaxAttempts times,
// sleeping an exponentially-growing (capped) delay between attempts and aborting
// early on ctx cancellation. The final attempt's error wraps as RetryExhausted.
// An error rejected by cfg.Retryable ends the loop at once and is returned
// verbatim, so a deterministic failure neither spends the budget nor hides
// behind RetryExhausted. When cfg.Delay reads a wait from the failure — a
// Retry-After, a RateLimited rejection — that wait replaces the backoff,
// held at cfg.MaxDelay.
func NewRetry(cfg RetryConfig) coreres.Runner {
	//: clamp the attempt budget to at least one.
	if cfg.MaxAttempts < minAttempts {
		//: a non-positive budget still runs once.
		cfg.MaxAttempts = minAttempts
	}
	//: a sub-1 multiplier would shrink the delay, and a NaN one would poison
	//: every product — both default to doubling, through the same
	//: normalisation the kernel curve applies.
	cfg.Multiplier = kbackoff.NormalMultiplier(cfg.Multiplier)
	//: NaN FIRST, inside NormalJitter: Go's min/max propagate it, so
	//: min(max(NaN, 0), 1) is NaN, and NaN <= 0 is false — it would sail past
	//: every later guard into a float-to-int conversion Go leaves
	//: implementation-defined. A jitter wider than the delay is a different
	//: policy, so the rest is clamped into [0, 1].
	cfg.Jitter = kbackoff.NormalJitter(cfg.Jitter)
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
			if waitErr := r.wait(ctx, attempt, lastErr); waitErr != nil {
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

// wait sleeps the delay before the given attempt on the configured clock,
// returning ctx.Err on cancel. failed is the error of the attempt before it.
func (r *retryRunner) wait(ctx context.Context, attempt int, failed error) error {
	//: a deadline-aware sleep on the injected clock, so a test drives the
	//: whole backoff with a ManualClock instead of sleeping through it.
	timer := timedOrSystem(r.cfg.Clock).NewTimer(r.delay(attempt, failed))
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

// delay is the wait before attempt: the one cfg.Delay reads from the failure
// when it reads one, held at MaxDelay and never negative, else the jittered
// backoff curve.
func (r *retryRunner) delay(attempt int, failed error) time.Duration {
	//: the failure decides first, when the caller asked it to.
	if r.cfg.Delay != nil {
		//: a delay the failure named replaces the curve, jitter included.
		if imposed, ok := r.cfg.Delay(attempt, failed); ok {
			//: held into [0, MaxDelay]; a zero MaxDelay is no cap.
			return capDelay(imposed, r.cfg.MaxDelay)
		}
	}
	//: the curve, as before the field existed.
	return r.jittered(r.backoff(attempt))
}

// capDelay holds d into [0, ceiling], a non-positive ceiling being no cap.
func capDelay(d, ceiling time.Duration) time.Duration {
	//: a negative wait is no wait.
	if d < 0 {
		//: retry at once.
		return 0
	}
	//: the caller's ceiling bounds what the other side may impose.
	if ceiling > 0 && d > ceiling {
		//: cut to the ceiling.
		return ceiling
	}
	//: within bounds.
	return d
}

// backoff computes the capped exponential delay for attempt (1-based growth).
// It is the deterministic half of the kernel curve (kernel/backoff.Grow — the
// curve BackoffValue publishes), built from this policy's four fields, so the
// two cannot drift apart.
func (r *retryRunner) backoff(attempt int) time.Duration {
	//: Grow normalises the multiplier NewRetry already normalised, which is
	//: idempotent and covers a runner built without NewRetry.
	return kbackoff.Grow(r.cfg.BaseDelay, r.cfg.MaxDelay, r.cfg.Multiplier, attempt)
}

// jittered widens delay by a uniform random fraction of itself, or returns it
// unchanged when no jitter is configured.
//
// It is separate from backoff so backoff stays a PURE function of the attempt
// number: the deterministic growth and the randomisation are two different
// claims, and a suite that cannot assert the first without the second can
// assert neither precisely.
func (r *retryRunner) jittered(delay time.Duration) time.Duration {
	//: NewRetry normalises NaN and clamps the range; Widen repeats it for a
	//: runner built without NewRetry.
	return kbackoff.Widen(delay, r.cfg.Jitter)
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
