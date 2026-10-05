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

// MaxDeadlineOffset is the furthest ahead of now a deadline built from a
// policy may lie: the ceiling on [PolicyValue.VisibilityTimeout] and
// [PolicyValue.RetryDelay], REFUSED above it rather than clamped.
//
// It is not a judgement about how long a lease should last — ADR 0054 argues
// against even an hour. It is the representable range, with a margin. Every
// deadline is now plus one of these durations, and a durable broker records it
// as Unix nanoseconds in an int64 — that is the form a filename carries, and
// time.Time.UnixNano's own range — which ends on 2262-04-11. Past that instant
// the number wraps negative, and a name carrying it cannot be read back: the
// message it names is never reclaimed and never delivered, and its receipt
// reads [UnknownReceipt]. math.MaxInt64, the value somebody reaches for to
// mean "never", is 292 years, so it wraps a deadline set today.
//
// A century keeps every deadline representable for any clock reading before
// 2162, and no lease or retry delay has a reading anywhere near it. Validate
// has no clock, so a fixed ceiling is the only bound it can check; an
// implementation that computes a deadline from its own clock at runtime — a
// lease extension, whose duration Validate never sees — checks the instant
// itself.
const MaxDeadlineOffset time.Duration = 100 * 365 * 24 * time.Hour

// Validate reports whether the policy is one a broker can honour, and refuses
// it BY FIELD when it is not.
//
// It lives in core so that the memory, file and SQL brokers refuse identical
// inputs identically — the property that makes the first usable as a test
// double for the other two. It is the same instrument core/data/vfs's ValidatePath
// and ValidatePerm are, for the same reason.
func (p PolicyValue) Validate() error {
	//: the lease lifetime, whose two zero readings are opposites — and whose
	//: deadline, past MaxDeadlineOffset, strands the message it names.
	if p.VisibilityTimeout <= 0 || p.VisibilityTimeout > MaxDeadlineOffset {
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
	//: every value of RetryDelay at or below the ceiling has exactly one
	//: sensible reading, a negative one included — it is "no delay", and
	//: Normalized says so. Above it, the retry deadline strands the message
	//: exactly as an unbounded lease would.
	if p.RetryDelay > MaxDeadlineOffset {
		//: QueueMisconfigured, naming the field.
		return errs.Wrap(QueueMisconfigured, errs.WrapParams{},
			errs.String("field", "RetryDelay"),
			errs.Int64("value_ns", int64(p.RetryDelay)))
	}
	//: a policy a broker can honour, once its growth is one too.
	return p.validateRetryGrowth()
}

// validateRetryGrowth refuses a MaxRetryDelay that is not a ceiling a retry
// delay can grow to. Zero asks for no growth and is accepted.
func (p PolicyValue) validateRetryGrowth() error {
	//: no growth asked for: RetryDelay stays what it always was.
	if p.MaxRetryDelay == 0 {
		//: nothing to check.
		return nil
	}
	var problem string
	//: the four ways a ceiling is no ceiling, most basic first.
	switch {
	//: not a bound at all, and the caller who wrote it meant something.
	case p.MaxRetryDelay < 0:
		problem = "negative"
	//: a retry deadline a durable broker could not write down.
	case p.MaxRetryDelay > MaxDeadlineOffset:
		problem = "past MaxDeadlineOffset"
	//: growth from zero is zero forever: a knob that does nothing.
	case p.RetryDelay <= 0:
		problem = "no RetryDelay to grow from"
	//: a ceiling under the first step would shorten every wait asked for.
	case p.MaxRetryDelay < p.RetryDelay:
		problem = "below RetryDelay"
	default:
		//: a curve from RetryDelay up to a representable ceiling.
		return nil
	}
	//: QueueMisconfigured, naming the field and what is wrong with it.
	return errs.Wrap(QueueMisconfigured, errs.WrapParams{},
		errs.String("field", "MaxRetryDelay"), errs.String("problem", problem),
		errs.Int64("value_ns", int64(p.MaxRetryDelay)))
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
