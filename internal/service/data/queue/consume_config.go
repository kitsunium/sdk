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

// DefaultSettleTimeout bounds one acknowledgement, nack, rejection or release
// [Consume] sends once its context has ended, and one lease renewal, when
// [ConsumerConfig.SettleTimeout] is left at zero.
//
// It is a CLAMP and not a refusal (ADR 0031): its zero has one reading — "you
// did not think about how long the medium may take to record an outcome" —
// and five seconds is far above what a flush-free rename or one indexed
// statement costs while still letting a stop end when the medium has stopped
// answering. It is also the most a handler is stopped early by: the margin
// kept before a lease ends is a tenth of the lease, at most the
// SettleTimeout in force.
const DefaultSettleTimeout time.Duration = 5 * time.Second

// ConsumerConfig configures [Consume]: what to run, how many of it, how often
// to look, and the one thing about the handler the SDK cannot check for
// itself.
//
// Every field except Handler and HandlerIsIdempotent is CLAMPED, so a caller
// with no opinion writes two fields and gets a correct consumer. The zero of
// each field added since — DrainTimeout, AutoExtend — is what Consume did
// before it existed.
type ConsumerConfig struct {
	// Handler processes one delivery. It must be non-nil.
	Handler corequeue.Handler
	// Clock drives the idle wait, the handler's lease bound, the drain, the
	// bound on each outcome and the lease renewals. Nil means clock.System.
	//
	// Nothing in this package calls time.Sleep; an idle worker waits through
	// Waiter.After and a cancelled context wakes it immediately.
	//
	// A lease's end is the broker's instant and is read on this clock, so it
	// must be the broker's: a test drives both with one ManualClock. A lease
	// that already looks ended on this clock the moment it is received —
	// two clocks that disagree — is not bounded at all, as before the bound
	// existed, rather than never run.
	Clock clock.Timed
	// PollInterval is how long a worker waits after finding the queue empty.
	// Zero means [DefaultPollInterval]; negative is read as zero.
	//
	// When the broker is a [corequeue.Waker] — every broker in this package
	// is — it is an upper bound and not a cadence: a Publish or a Nack in
	// this process wakes the worker at once, and a retry delay ending or a
	// lease lapsing wakes it at that instant. What is left for the poll to
	// find is a publication another process made into a durable queue, so set
	// it to how late such a message may be noticed — seconds, not
	// milliseconds, is the usual answer — and an idle consumer then costs
	// nothing between polls.
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
	//
	// A message whose lease has run down to the margin [Consume] keeps
	// before its turn comes is not started: it is handed back uncounted
	// and leased again by a later poll — or, with AutoExtend, renewed
	// before it starts.
	BatchSize int
	// SettleTimeout bounds each outcome a worker reports once ctx has
	// ended — an acknowledgement, a nack, a rejection or a release — and
	// each lease renewal AutoExtend sends. Zero means
	// [DefaultSettleTimeout]; negative is read as zero.
	//
	// Every outcome is sent on the consumer's context WITHOUT its
	// cancellation, so a stop landing between a handler's return and its
	// acknowledgement does not abandon it (kitsunium/sdk#472) and a batch a
	// stop interrupts is still handed back. That is what needs a bound: a
	// medium that stopped answering would otherwise hold the stop forever.
	// It runs on Clock, from the instant ctx ends — while the consumer
	// runs an outcome is not timed, and nothing but the worker waits on
	// it. An outcome the bound cut short is QueueBackendFailed, which a
	// stopping consumer reads as its stop, not as a failure.
	//
	// It also caps the margin a handler is stopped by before its lease
	// ends: a tenth of the lease, at most this.
	SettleTimeout time.Duration
	// DrainTimeout is how long the message a worker is handling when ctx
	// ends may still run before its handler's context is cancelled — River's
	// SoftStopTimeout. Zero cancels it at once, which is what a stop always
	// did; negative is read as zero.
	//
	// A handler that finishes inside it is acknowledged; one the drain cut
	// short is handed back uncounted, through the broker's
	// [corequeue.Releaser]. No new message is leased once ctx has ended,
	// and the lease bound still applies: a drain never keeps a handler past
	// the margin before its lease ends. Measured on Clock. A caller's stop
	// budget — a lifecycle component's — must cover it plus SettleTimeout.
	DrainTimeout time.Duration
	// AutoExtend renews the lease of the message a handler is running, a
	// third of the lease after it was granted or last renewed, for as long
	// as the handler runs — the heartbeat of SQS, JetStream's in-progress
	// acknowledgement, Temporal's activity heartbeat. A message of a batch
	// whose lease is a third spent when its turn comes is renewed before
	// its handler starts. Each renewal is as long as the lease the broker
	// first granted, and is bounded by SettleTimeout.
	//
	// It needs the broker's [corequeue.LeaseExtender]; without it, or when
	// false, the lease is never renewed and the handler is stopped a margin
	// before it ends. A renewal the broker refuses — the lease lapsed, the
	// medium failed — cancels the handler's context with that refusal as
	// its cause: its work is now a duplicate in the making.
	//
	// A renewal replaces the receipt, and the engine acknowledges with the
	// newest one. So a handler that extends its own lease — parking its
	// message, as the mail spool does — must leave AutoExtend false: the
	// receipt it holds is void after the first renewal.
	AutoExtend bool
	// HandlerIsIdempotent asserts, IN CODE, that Handler is safe to run more
	// than once for the same message.
	//
	// It must be true. Its zero value is false and is REFUSED
	// ([corequeue.ConsumerMisconfigured]), and that refusal is the domain's
	// one piece of deliberate ceremony.
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
	//: zero asks for the default bound; an outcome is never sent unbounded.
	if c.SettleTimeout <= 0 {
		c.SettleTimeout = DefaultSettleTimeout
	}
	//: negative is no drain, which is what a stop always was.
	c.DrainTimeout = max(c.DrainTimeout, 0)
	//: nil is the caller with no opinion.
	if c.Clock == nil {
		c.Clock = clock.System
	}
	//: the values a worker actually runs on.
	return c
}
