// Package queue — hosts the delivery policy every broker is configured with,
// and the guard both implementations run so that the two refuse identical
// inputs identically.
package queue

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// DefaultMaxMessageBytes bounds a payload when [PolicyValue.MaxMessageBytes]
// is left at zero. One mebibyte is the value the industry converged on —
// NATS's default, Kafka's default `message.max.bytes`, four times SQS's
// maximum — and the reason it is a CLAMP rather than a refusal is that its
// zero has exactly one sensible reading and the wrong answer is survivable:
// too small is a refused publish at the call site that made the mistake, and
// the alternative to any bound at all is one producer filling a disk.
const DefaultMaxMessageBytes int = 1 << 20

// PolicyValue is the delivery discipline a broker enforces: how long a lease
// lasts, how many attempts a message gets, how long a failure waits, and how
// large a payload may be.
//
// # Its zero value is REFUSED, and that is the decision
//
// ADR 0031 gives two branches — clamp where a working default needs no
// explanation, refuse where any value the SDK invented would be arbitrary —
// and this one struct uses both, which is why they are worth reading side by
// side.
//
//   - [PolicyValue.VisibilityTimeout] and [PolicyValue.MaxDeliveries] are
//     REFUSED at zero. Each has two natural readings that are OPPOSITES, and
//     in each case one of them silently destroys the guarantee: a zero
//     visibility timeout is either "redeliver immediately", which is a
//     delivery storm, or "never redeliver", which is a lost message; zero
//     deliveries is either "unlimited", which lets one poison message loop
//     forever and the dead-letter store stay empty, or "none", which is a
//     queue that delivers nothing. This is core/lock's argument for refusing
//     a zero TTL, and it is the same argument because it is the same class of
//     value: a lifetime belongs to the work being protected, and only the
//     caller knows what that is.
//   - [PolicyValue.RetryDelay] and [PolicyValue.MaxMessageBytes] are CLAMPED.
//     A zero retry delay means "as soon as the lease lapses", which is one
//     reading, is harmless, and is what a caller who has not thought about
//     backoff wants. A zero size bound means [DefaultMaxMessageBytes].
//
// Both branches in one struct is the point. A domain in which every zero is
// refused teaches a reader nothing except that the author was nervous.
type PolicyValue struct {
	// VisibilityTimeout is how long a [Broker.Receive] hides a message from
	// every other consumer. It is the deadline the consumer is racing, and
	// it must be longer than the slowest run of the handler — or the handler
	// must renew it through [LeaseExtender].
	//
	// It is the single knob that sets how quickly a dead consumer's work is
	// picked up: a message held by a process that has been SIGKILLed
	// reappears exactly this long after it was leased, and not before.
	//
	// A non-positive value is refused ([QueueMisconfigured]).
	VisibilityTimeout time.Duration
	// RetryDelay is how long a NACKED message stays invisible before it
	// becomes eligible again, measured from the nack.
	//
	// It does NOT apply to a lease that simply lapsed: that message has
	// already been invisible for a whole visibility timeout, and charging it
	// a second wait would punish a crashed consumer more than a failing one.
	//
	// Zero is a working value and means "eligible as soon as the nack
	// returns". Negative is read as zero.
	RetryDelay time.Duration
	// MaxDeliveries is how many times one message may be handed to a
	// consumer before the broker gives up and dead-letters it. It is
	// compared against [DeliveryValue.Deliveries], which counts from 1, so a
	// value of 1 means "one attempt, then the dead-letter store" and is a
	// legitimate configuration rather than an off-by-one.
	//
	// A non-positive value is refused ([QueueMisconfigured]).
	MaxDeliveries int
	// MaxMessageBytes bounds one payload. A larger payload is refused at
	// [Broker.Publish] with [MessageTooLarge], at the call site that built
	// it, rather than accepted and discovered by a consumer that cannot read
	// it back.
	//
	// Zero means [DefaultMaxMessageBytes]. Negative is refused, because it
	// is not a bound at all and the caller who wrote it meant something.
	MaxMessageBytes int
}

// Validate reports whether the policy is one a broker can honour, and refuses
// it BY FIELD when it is not.
//
// It lives in core so that the memory broker and the file broker refuse
// identical inputs identically — the property that makes the first usable as
// a test double for the second. It is the same instrument core/vfs's
// ValidatePath and ValidatePerm are, for the same reason.
func (p PolicyValue) Validate() error {
	//: the lease lifetime, whose two zero readings are opposites.
	if p.VisibilityTimeout <= 0 {
		//: QueueMisconfigured, naming the field.
		return errs.Wrap(QueueMisconfigured, errs.WrapParams{},
			errs.String("field", "VisibilityTimeout"),
			errs.Int64("value_ns", int64(p.VisibilityTimeout)))
	}
	//: the attempt budget, whose two zero readings are also opposites.
	if p.MaxDeliveries <= 0 {
		//: QueueMisconfigured, naming the field.
		return errs.Wrap(QueueMisconfigured, errs.WrapParams{},
			errs.String("field", "MaxDeliveries"), errs.Int("value", p.MaxDeliveries))
	}
	//: a negative bound is not a bound; zero is the clamp and is accepted.
	if p.MaxMessageBytes < 0 {
		//: QueueMisconfigured, naming the field.
		return errs.Wrap(QueueMisconfigured, errs.WrapParams{},
			errs.String("field", "MaxMessageBytes"), errs.Int("value", p.MaxMessageBytes))
	}
	//: RetryDelay is deliberately absent: every value of it, including a
	//: negative one, has exactly one sensible reading.
	return nil
}

// Normalized returns the policy with its clamped fields resolved, so that
// every implementation reads the same numbers rather than each remembering to
// apply the same default.
//
// It does NOT validate: call [PolicyValue.Validate] first, at construction,
// where the refusal reaches the caller who wrote the mistake.
func (p PolicyValue) Normalized() PolicyValue {
	//: a negative delay is "no delay"; there is nothing else it could mean.
	if p.RetryDelay < 0 {
		p.RetryDelay = 0
	}
	//: zero is the documented request for the default bound.
	if p.MaxMessageBytes == 0 {
		p.MaxMessageBytes = DefaultMaxMessageBytes
	}
	//: the values every broker in this SDK actually enforces.
	return p
}
