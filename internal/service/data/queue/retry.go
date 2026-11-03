package queue

import (
	"math/rand/v2"
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	kbackoff "github.com/kitsunium/sdk/internal/kernel/backoff"
)

// decorrelatedFactor is how far above the previous wait a decorrelated draw
// may reach: kernel/backoff's, the Builders' Library's `sleep * 3`.
const decorrelatedFactor float64 = 3

// retryDelay is how long a message nacked on its deliveries-th delivery stays
// invisible: the policy's RetryDelay, or — when MaxRetryDelay asks for growth
// — the SDK's one backoff curve from RetryDelay up to MaxRetryDelay (ADR 0103,
// ADR 0151), doubling with each delivery that failed, randomised as
// RetryJitterMode says from one draw of random.
//
// It is one function the three brokers call rather than three copies of a
// comparison, for the reason core/data/queue.PolicyValue.Validate lives in core:
// the memory broker is a double only as long as it waits exactly what the
// durable ones wait. policy is normalised, so RetryDelay is never negative,
// and validated, so a growing delay has a positive RetryDelay below its
// ceiling and a jitter only beside a ceiling. The draw is the broker's
// injected random, never kernel/backoff's own: a retry instant is part of
// what Wake reports and of what a test asserts to the nanosecond, so a test
// that injects a fixed draw still can (issue #1044). With RetryJitter zero
// and the zero mode the instant is the one the curve gave before the jitter
// existed, and random is not called.
func retryDelay(policy corequeue.PolicyValue, deliveries int, random func() float64) time.Duration {
	//: no ceiling asked for: the constant delay every policy had before.
	if policy.MaxRetryDelay <= 0 {
		//: RetryDelay, whatever the attempt.
		return policy.RetryDelay
	}
	//: RetryDelay × 2^(deliveries−1), held at the ceiling.
	grown := kbackoff.Grow(policy.RetryDelay, policy.MaxRetryDelay, 0, deliveries)
	//: the randomisation the policy names, over the curve.
	switch policy.RetryJitterMode {
	//: the whole wait drawn below the curve.
	case kbackoff.JitterFull:
		//: uniform in [0, grown].
		return drawBetween(0, grown, random)
	//: the wait drawn from the curve's step before, as kernel/backoff's
	//: memoryless Delay reads it.
	case kbackoff.JitterDecorrelated:
		previous := kbackoff.Grow(policy.RetryDelay, policy.MaxRetryDelay, 0, deliveries-1)
		upper := policy.MaxRetryDelay
		//: compared in the float domain before any conversion: three times a
		//: century overflows a Duration.
		if bound := float64(previous) * decorrelatedFactor; bound < float64(upper) {
			upper = time.Duration(bound)
		}
		//: uniform in [RetryDelay, min(3 × previous, MaxRetryDelay)].
		return drawBetween(policy.RetryDelay, max(upper, policy.RetryDelay), random)
	//: additive, the zero.
	default:
		//: no fraction asked for: the deterministic curve, no draw made.
		if policy.RetryJitter <= 0 {
			//: the instant the policy always gave.
			return grown
		}
		//: grown ≤ MaxDeadlineOffset and the fraction ≤ 1, so the sum stays
		//: far inside a Duration.
		return drawBetween(grown, grown+time.Duration(float64(grown)*policy.RetryJitter), random)
	}
}

// drawBetween returns a duration in [low, high] from one draw of random, a
// number in [0, 1). A draw outside it — a broken source — is held inside, so
// no source makes a retry wait less than low or more than high.
func drawBetween(low, high time.Duration, random func() float64) time.Duration {
	//: nothing to spread.
	if high <= low {
		//: the one instant there is.
		return low
	}
	fraction := min(max(random(), 0), 1)
	//: span+1 reaches high itself; the product converts in range because the
	//: span is at most twice a century.
	return min(low+time.Duration(fraction*float64(high-low+1)), high)
}

// randomOrDefault is a configuration's Random, or math/rand/v2's Float64 when
// it named none.
func randomOrDefault(random func() float64) func() float64 {
	//: nil is the caller with no opinion.
	if random == nil {
		//: the process-wide source.
		return rand.Float64
	}
	//: the caller's draw — a test's fixed sequence.
	return random
}
