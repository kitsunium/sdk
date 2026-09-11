// Package queue — the ADR 0039 capability siblings of the frozen [Broker]
// port, and the dead-letter record one of them returns.
package queue

import (
	"context"
	"time"
)

// DeadLetterReader reads the messages a broker gave up on.
//
// It is a SIBLING of [Broker] reached by type assertion, never a fifth method
// on it, because reading the dead letters back is a capability and not every
// broker has one: a connector onto a third-party system usually dead-letters
// into a second queue that is read with the same [Broker] interface, and one
// that dead-letters into an operator's console cannot return anything at all.
// Both brokers in internal/service/queue implement it.
//
//	if reader, ok := broker.(queue.DeadLetterReader); ok {
//		dead, err := reader.DeadLetters(ctx, 100)
//	}
type DeadLetterReader interface {
	// DeadLetters returns up to max dead letters, oldest first. It does NOT
	// remove them: a dead letter is evidence, and a read that consumed it
	// would make the second investigator's job impossible.
	//
	// max must be positive ([InvalidBatchSize]).
	DeadLetters(ctx context.Context, max int) ([]DeadLetterValue, error)
}

// LeaseExtender renews a lease a handler is still working under.
//
// It is the honest answer to the one question [PolicyValue.VisibilityTimeout]
// cannot settle: the timeout must be longer than the slowest handler, and the
// slowest handler is not known in advance. Rather than inviting a caller to
// set an hour-long visibility timeout — which turns every consumer crash into
// an hour of silence — a handler that legitimately needs longer says so.
//
// It is a sibling for the same reason [DeadLetterReader] is: a broker over a
// transport with no renewal verb cannot implement it, and the assertion is
// how a caller finds that out. Both brokers in internal/service/queue
// implement it.
type LeaseExtender interface {
	// Extend renews the lease named by receipt for a further by, measured
	// from now, and returns the lease that replaces it.
	//
	// It returns a NEW [LeaseValue] — a new receipt included — and the old
	// receipt is void from that moment. That is not ceremony: an
	// implementation whose lease deadline lives in a durable name can only
	// change it by replacing the name, and a receipt that silently kept
	// meaning something after the thing it named had moved would be the one
	// bug this whole domain is built to avoid.
	//
	// A lapsed lease is [LeaseExpired] and is NOT renewed, even when no
	// other consumer has taken the message yet: expiry is a deadline, not
	// "until somebody else wants it". The handler that asks and is refused
	// has learnt that its work is now a duplicate, which is exactly when it
	// needed to know.
	//
	// A non-positive by is [QueueMisconfigured]: the two readings of zero —
	// "lapse now" and "never lapse" — are opposites and both harmful.
	Extend(ctx context.Context, receipt ReceiptValue, by time.Duration) (LeaseValue, error)
}

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
	// errs.Error Reason of the cause, or LEASE_EXPIRED when the message
	// died by exhausting its deliveries without any consumer ever reporting
	// anything.
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
	// this SDK's brokers produce.
	Deliveries int
}
