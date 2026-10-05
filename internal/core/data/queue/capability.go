package queue

import (
	"time"
)

// DeadLetterValue is one message the broker gave up on, together with why.
//
// A dead letter without its cause is an investigation nobody can run, so the
// cause is part of the record rather than a line in whatever log has since
// rotated away. What is kept of the cause is CLAUDE.md rule 4's split, and
// the split is deliberate: the wire-safe Public half and the dotted-quad code
// go to the store, the Private half stays in the log of the process that
// failed, and [MessageValue.ID] joins the two. A dead-letter store is read by
// whoever is investigating — frequently not the process, the host or the
// trust domain that produced the failure — so it is the wrong place for the
// half of an error the SDK defines as log-only.
type DeadLetterValue struct {
	// FailedAt is when the last attempt failed and the message was moved.
	FailedAt time.Time
	// Reason is the SCREAMING_SNAKE identifier of the failure — the
	// errs.Error Reason of the cause, NOT_RETRYABLE for a handler's
	// [DoNotRetry] around an error that carried none, or LEASE_EXPIRED when
	// the message died by exhausting its deliveries without any consumer ever
	// reporting anything.
	Reason string
	// Cause is the wire-safe Public half of the last failure, or an empty
	// string when the consumer reported no cause at all. It never carries
	// the message payload, which is a security property this domain shares
	// with the rest of the SDK: a queue moves bytes a consumer must not
	// leak, and an error message is the classic place they leak from.
	Cause string
	// Message is the work, unmodified, with the identifier it was published
	// under.
	Message MessageValue
	// Code is the dotted-quad errs.Code of the last failure, or 0 when the
	// cause carried none. It is a uint32 rather than an errs.Code so that
	// this package's values stay free of the kernel's error type in a shape
	// a consumer may serialise; errs.Code(dl.Code) converts.
	Code uint32
	// Deliveries is how many times the message was delivered before it was
	// abandoned. It equals [PolicyValue.MaxDeliveries] for every dead letter
	// this SDK's brokers produce by running out of attempts, and is the count
	// the message had for one a handler rejected ([Rejecter]).
	Deliveries int
}
