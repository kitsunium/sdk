// Package queue — the consumer engine's configuration, its one mandatory
// assertion, and the clamps that make every other field safe to omit.
package queue

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
)

// DefaultPollInterval is how long an idle worker waits before asking again,
// when [ConsumerConfig.PollInterval] is left at zero.
//
// It is a CLAMP and not a refusal (ADR 0031): its zero has one reading — "you
// did not think about polling" — and 100 ms is defensible for every queue,
// because it bounds idle latency at a tenth of a second while costing one
// directory read per worker per tenth of a second. A caller who cares picks
// their own.
const DefaultPollInterval time.Duration = 100 * time.Millisecond

// ConsumerConfig configures [Consume]: what to run, how many of it, how often
// to look, and the one thing about the handler the SDK cannot check for
// itself.
//
// Every field except Handler and HandlerIsIdempotent is CLAMPED, so a caller
// with no opinion writes two fields and gets a correct consumer.
type ConsumerConfig struct {
	// Handler processes one delivery. It must be non-nil.
	Handler corequeue.Handler
	// Clock drives the idle wait. Nil means clock.System.
	//
	// Nothing in this package calls time.Sleep; an idle worker waits through
	// Waiter.After and a cancelled context wakes it immediately.
	Clock clock.Timed
	// PollInterval is how long a worker waits after finding the queue empty.
	// Zero means [DefaultPollInterval]; negative is read as zero.
	PollInterval time.Duration
	// Parallelism is how many independent workers run. Each one polls, leases
	// and processes on its OWN goroutine, which is what makes this the
	// consumer's goroutine rather than the publisher's.
	//
	// Non-positive is clamped to 1: one worker is what a caller who did not
	// think about concurrency wants, it is never wrong, and there is nothing
	// here for the SDK to invent.
	//
	// Above 1 there is NO ordering left, in any sense.
	Parallelism int
	// BatchSize is how many messages one worker leases per poll. Non-positive
	// is clamped to 1.
	//
	// Above 1 a worker processes its batch SEQUENTIALLY, so the extra
	// messages are held under a lease they are not yet being worked on: a
	// batch of 50 with a 30-second visibility timeout gives the last message
	// 30 seconds minus the other 49's processing time. Batch for throughput
	// against a durable broker, where one poll is a directory read; do not
	// batch to get parallelism, which is what Parallelism is.
	BatchSize int
	// HandlerIsIdempotent asserts, IN CODE, that Handler is safe to run more
	// than once for the same message.
	//
	// It must be true. Its zero value is false and is REFUSED
	// ([ConsumerMisconfigured]), and that refusal is the domain's one piece
	// of deliberate ceremony.
	//
	// The reason is that this queue delivers AT LEAST ONCE and cannot do
	// otherwise. A consumer that dies between finishing the work and
	// acknowledging it has done the work and not said so, and the message
	// comes back — that is not a bug to be fixed, it is the only honest
	// behaviour available to a queue whose consumers can die. Exactly-once
	// delivery does not exist over a transport; what exists is at-least-once
	// plus an idempotent consumer, and the second half is the caller's.
	//
	// The SDK cannot check it, so it makes the caller assert it where a
	// reviewer will see it. It is precisely
	// resilience.HedgeConfig.Idempotent's instrument, for precisely its
	// reason, and it has the same useful side effect:
	// `grep -rn HandlerIsIdempotent` enumerates every place in a codebase
	// where somebody promised this.
	HandlerIsIdempotent bool
}

// validate refuses a consumer wiring that could never run correctly.
func (c ConsumerConfig) validate() error {
	//: a nil handler is a consumer that leases messages and drops them.
	if c.Handler == nil {
		//: ConsumerMisconfigured, naming the field.
		return kerrs.Wrap(ConsumerMisconfigured, kerrs.WrapParams{}, kerrs.String("field", "Handler"))
	}
	//: THE assertion. Its zero value is the conservative reading, so a caller
	//: cannot acquire the promise by omission.
	if !c.HandlerIsIdempotent {
		//: ConsumerMisconfigured, naming the field.
		return kerrs.Wrap(ConsumerMisconfigured, kerrs.WrapParams{},
			kerrs.String("field", "HandlerIsIdempotent"), kerrs.Bool("value", c.HandlerIsIdempotent))
	}
	//: runnable.
	return nil
}

// normalized resolves the clamped fields, so every worker reads the same
// numbers rather than each remembering to apply the same default.
func (c ConsumerConfig) normalized() ConsumerConfig {
	//: one worker is what a caller who did not think about concurrency wants.
	c.Parallelism = max(c.Parallelism, 1)
	//: one message per poll, likewise.
	c.BatchSize = max(c.BatchSize, 1)
	//: zero is the documented request for the default cadence.
	if c.PollInterval <= 0 {
		c.PollInterval = DefaultPollInterval
	}
	//: nil is the caller with no opinion.
	if c.Clock == nil {
		c.Clock = clock.System
	}
	//: the values a worker actually runs on.
	return c
}
