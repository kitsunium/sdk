package spool

import (
	"context"
)

// What can happen to a spooled mail. The zero value is none of them.
const (
	// EventQueued: Send or SendWithID put the mail in the spool.
	EventQueued EventKind = iota + 1
	// EventSent: the transport accepted the mail.
	EventSent
	// EventRetrying: an attempt failed and the next is due at Next.
	EventRetrying
	// EventDeadLettered: the last attempt failed; the mail is kept, with
	// that failure, and never delivered again.
	EventDeadLettered
	// EventDuplicate: the queue handed back a mail under an identifier this
	// spool had already delivered — its lease lapsed while the relay was
	// accepting it, or a SendWithID repeated the identifier — and it was
	// dropped instead of sent twice. A repeat's own EventQueued comes first,
	// so an observer keyed by identifier reads this one as "delivered
	// already".
	EventDuplicate
)

// String renders the kind for a log line; a value this package never mints
// renders as "unknown".
func (k EventKind) String() string {
	//: a closed set of five, plus the honest answer for anything else.
	switch k {
	//: queued.
	case EventQueued:
		//: queued.
		return "queued"
	//: sent.
	case EventSent:
		//: sent.
		return "sent"
	//: retrying.
	case EventRetrying:
		//: retrying.
		return "retrying"
	//: dead-lettered.
	case EventDeadLettered:
		//: dead-lettered.
		return "dead-lettered"
	//: dropped as a duplicate.
	case EventDuplicate:
		//: duplicate.
		return "duplicate"
	//: a value this package never mints.
	default:
		//: name the absence.
		return "unknown"
	}
}

// attemptKey keys the AttemptValue in a delivery's context.
type attemptKey struct{}

// AttemptFrom returns the attempt a delivery context carries, and whether it
// carries one: only a context the spool handed its transport does.
func AttemptFrom(ctx context.Context) (attempt AttemptValue, ok bool) {
	attempt, ok = ctx.Value(attemptKey{}).(AttemptValue)
	//: the attempt, or the zero value and false.
	return attempt, ok
}

// withAttempt returns ctx carrying attempt.
func withAttempt(ctx context.Context, attempt AttemptValue) context.Context {
	//: one value, under the package's own key.
	return context.WithValue(ctx, attemptKey{}, attempt)
}
