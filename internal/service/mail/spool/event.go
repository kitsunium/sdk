// Package spool — what a spool tells its observer, what an attempt knows
// about itself, and what a dead letter keeps.
package spool

import (
	"context"
	"time"

	coremail "github.com/kitsunium/sdk/internal/core/mail"
)

// EventKind names what an [EventValue] reports.
type EventKind uint8

// What can happen to a spooled mail. The zero value is none of them.
const (
	// EventQueued: Send put the mail in the spool.
	EventQueued EventKind = iota + 1
	// EventSent: the transport accepted the mail.
	EventSent
	// EventRetrying: an attempt failed and the next is due at Next.
	EventRetrying
	// EventDeadLettered: the last attempt failed; the mail is kept, with
	// that failure, and never delivered again.
	EventDeadLettered
	// EventDuplicate: the queue handed back a mail this spool had already
	// delivered — its lease lapsed while the relay was accepting it — and it
	// was dropped instead of sent twice.
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

// EventValue is one thing that happened to one mail. The mail, its metadata
// and its queue time travel with every event, because a spool may deliver a
// mail an earlier process queued, and an observer building a mailbox has
// seen no Send for it.
type EventValue struct {
	// Err is the failed attempt's error, for EventRetrying and
	// EventDeadLettered.
	Err error
	// Meta is what Config.Annotate returned when the mail was sent.
	Meta map[string]string
	// At is when it happened; QueuedAt when the mail was queued; Next when
	// the next attempt is due, for EventRetrying.
	At, QueuedAt, Next time.Time
	// ID is the spool's identifier for the mail.
	ID string
	// Message is the mail, as it was stamped at Send: sender, date and
	// Message-ID filled in.
	Message coremail.MessageValue
	// Attempt numbers the delivery attempt, from 1; zero for EventQueued.
	Attempt int
	// Kind says what happened.
	Kind EventKind
}

// AttemptValue is what one delivery attempt knows about itself. The spool
// puts it in the context it hands the transport, so a transport — or a
// framework's wrapper around one — can continue the trace of the Send, name
// the attempt in a log, or refuse a mail it has seen before.
type AttemptValue struct {
	// QueuedAt is when Send queued the mail.
	QueuedAt time.Time
	// Meta is what Config.Annotate returned at Send.
	Meta map[string]string
	// ID is the spool's identifier for the mail.
	ID string
	// Attempt numbers this attempt, from 1.
	Attempt int
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

// DeadLetterValue is a mail the spool gave up on, with why: the mail as it was
// spooled, when it was queued and when it failed for the last time, and that
// failure's reason, wire-safe cause and code.
type DeadLetterValue struct {
	// Meta is what Config.Annotate returned at Send.
	Meta map[string]string
	// FailedAt is when the last attempt failed; QueuedAt when the mail was
	// queued.
	FailedAt, QueuedAt time.Time
	// ID is the spool's identifier for the mail — empty when the record did
	// not decode, and QueueID then names it.
	ID string
	// QueueID is the queue's identifier for the record.
	QueueID string
	// Reason and Cause are the last failure's SCREAMING_SNAKE reason and its
	// wire-safe Public text; the Private half stayed in the failing
	// process's log.
	Reason, Cause string
	// Message is the mail, as it was spooled.
	Message coremail.MessageValue
	// Attempts is how many deliveries it got.
	Attempts int
	// Code is the last failure's dotted-quad code, or 0.
	Code uint32
}
