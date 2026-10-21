package resilience

import (
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// RetryConfig parameterises NewRetry. A non-positive MaxAttempts clamps to 1; a
// Multiplier of 1 or below (or NaN) defaults to 2; a zero MaxDelay means no
// cap; a nil Retryable replays every error; a zero Jitter backs off
// deterministically; a nil Clock waits on the wall clock; a nil Delay
// always waits the backoff curve; a nil Budget never refuses a retry; a nil
// OnRetry hears nothing.
//
// BaseDelay, MaxDelay, Multiplier and Jitter are the four fields of
// BackoffValue, under the same names and with the same meaning: NewRetry builds its curve
// from them, so a loop that retries on its own terms and a Retry policy
// configured alike wait alike.
type RetryConfig struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Multiplier  float64
	// Retryable classifies a non-nil operation error as replayable. It is
	// consulted once per failed attempt: false returns that error verbatim —
	// no backoff, no further attempt, no RetryExhausted relabel — so a
	// deterministic failure (policy refusal, HTTP 400, invalid input) surfaces
	// as itself at the first attempt instead of costing the whole budget. A
	// nil predicate replays every error, which is the pre-classifier
	// behaviour. BreakerConfig accepts the same shape, so one classifier
	// serves a nested Retry(Breaker(op)) stack.
	Retryable func(error) bool
	// Jitter widens each backoff by a random fraction of itself, drawn
	// uniformly from [0, Jitter) and ADDED to the computed delay. 0 — the zero
	// value — is the deterministic backoff this policy has always had, so an
	// existing caller's timing is unchanged. 0.5 is the common choice: it
	// desynchronises callers that failed together without letting the average
	// wait stop growing monotonically. Values outside [0, 1] clamp into it.
	//
	// It exists because deterministic backoff SYNCHRONISES the very callers it
	// is meant to spread. N clients that fail against one dependency at the
	// same instant retry at the same instant, N times over, and every retry
	// round lands as one burst on a dependency that is already struggling.
	// Jitter is what turns those bursts back into a distribution.
	//
	// It applies AFTER the MaxDelay cap, deliberately: the cap bounds the
	// growth, and jitter spreads callers around the bound rather than being
	// squeezed flat against it. The consequence is worth stating — WHEN a cap is
	// configured, the effective ceiling on one wait becomes MaxDelay * (1 +
	// Jitter), not MaxDelay. A zero MaxDelay is no cap at all, so there is no
	// ceiling to raise: the backoff grows geometrically and the jitter widens
	// whatever it reaches, bounded only by what a time.Duration can represent.
	Jitter float64
	// Clock is what the backoff waits on. Nil means clock.System.
	//
	// It exists so a test drives the whole retry loop with a ManualClock —
	// advancing past each backoff instead of sleeping through it, and asserting
	// the exact wait rather than a wall-clock tolerance. The breaker and the
	// rate limiter have taken a clock since they shipped; the retry, which is
	// the one policy here that actually WAITS, did not.
	Clock clock.Timed
	// Delay decides the wait before a retry when the failure itself says how
	// long to wait. It is called once per retry, with the retry's number —
	// 1 before the second attempt — and the error of the attempt that just
	// failed; a true second result makes its duration the wait, in place of
	// the backoff curve and without Jitter. A false one, and a nil Delay,
	// wait the curve, which is the behaviour before the field existed.
	//
	// The wait it returns is held at MaxDelay when one is set, and a
	// negative one is no wait. A server's hint longer than the caller is
	// prepared to wait is therefore cut to MaxDelay, never followed past
	// it; a caller who would rather give up classifies that error as not
	// retryable instead.
	//
	// RetryAfter has its shape, so honouring a Retry-After, an OTLP
	// collector's throttling or a RateLimited rejection is one line:
	//
	//	Delay: func(_ int, err error) (time.Duration, bool) { return resilience.RetryAfter(err) }
	Delay func(attempt int, err error) (time.Duration, bool)
	// Budget, when set, bounds the retries of every policy sharing it: each
	// success and each retryable failure is recorded in it, and before
	// every retry it is asked whether one may run. A refusal ends the loop
	// with RetryBudgetExceeded wrapping the last error, without waiting. A
	// deterministic error and a cancelled context record nothing.
	//
	// Nil — the zero value — is the policy without a budget, as it was
	// before the field existed. Share one budget per dependency, between
	// its retry and its hedge: that is what stops a failing dependency from
	// receiving every call times MaxAttempts. A Retry(Hedge(op)) whose two
	// policies share a budget records a failed call at both levels, which
	// drains and refills it twice as fast and leaves its threshold where it
	// was.
	Budget coreres.RetryBudget
	// OnRetry, when set, is called before each retry's wait, on the
	// calling goroutine, with the retry's number — 1 before the second
	// attempt —, the error that caused it and the wait about to start. It
	// is how a caller counts retries in their own metrics without this
	// package depending on one. It is not called for the first attempt,
	// nor for a retry the budget refused. It must not block: the retry
	// waits for it.
	OnRetry func(attempt int, err error, delay time.Duration)
}
