package queue

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// DefaultPollInterval is how long an idle worker waits before asking again,
// when [ConsumerConfig.PollInterval] is left at zero.
//
// It is a CLAMP and not a refusal (ADR 0031): its zero has one reading — "you
// did not think about polling" — and 100 ms is defensible for every queue,
// because it bounds idle latency at a tenth of a second while costing one
// directory read per worker per tenth of a second. A caller who cares picks
// their own.
//
// Every broker in this package is a [corequeue.Waker], so for them it bounds
// only what a wake cannot see — a publication made by ANOTHER process into a
// durable queue — and a caller that has no such producer can raise it a
// hundredfold. It stays at 100 ms because a zero must keep meaning what it
// always meant for a broker that cannot wake anyone.
const DefaultPollInterval time.Duration = 100 * time.Millisecond

// validate refuses a consumer wiring that could never run correctly.
func (c ConsumerConfig) validate() error {
	//: a nil handler is a consumer that leases messages and drops them.
	if c.Handler == nil {
		//: ConsumerMisconfigured, naming the field.
		return kerrs.Wrap(corequeue.ConsumerMisconfigured, kerrs.WrapParams{}, kerrs.String("field", "Handler"))
	}
	//: THE assertion. Its zero value is the conservative reading, so a caller
	//: cannot acquire the promise by omission.
	if !c.HandlerIsIdempotent {
		//: ConsumerMisconfigured, naming the field.
		return kerrs.Wrap(corequeue.ConsumerMisconfigured, kerrs.WrapParams{},
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
