// Package queue — how long a nacked message waits, the same way in all three
// brokers.
package queue

import (
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
	kbackoff "github.com/kitsunium/sdk/internal/kernel/backoff"
)

// retryDelay is how long a message nacked on its deliveries-th delivery stays
// invisible: the policy's RetryDelay, or — when MaxRetryDelay asks for growth
// — the SDK's one backoff curve from RetryDelay up to MaxRetryDelay (ADR 0103,
// ADR 0151), doubling with each delivery that failed.
//
// It is one function the three brokers call rather than three copies of a
// comparison, for the reason core/queue.PolicyValue.Validate lives in core:
// the memory broker is a double only as long as it waits exactly what the
// durable ones wait. policy is normalised, so RetryDelay is never negative,
// and validated, so a growing delay has a positive RetryDelay below its
// ceiling. The curve's jitter stays at zero: a retry instant is part of what
// Wake reports and of what a test asserts to the nanosecond.
func retryDelay(policy corequeue.PolicyValue, deliveries int) time.Duration {
	//: no ceiling asked for: the constant delay every policy had before.
	if policy.MaxRetryDelay <= 0 {
		//: RetryDelay, whatever the attempt.
		return policy.RetryDelay
	}
	//: RetryDelay × 2^(deliveries−1), held at the ceiling.
	return kbackoff.Value{BaseDelay: policy.RetryDelay, MaxDelay: policy.MaxRetryDelay}.Delay(deliveries)
}
