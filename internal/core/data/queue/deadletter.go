// Package queue — the two ADR 0039 siblings that act on the dead-letter store:
// sending a message there at once, and deciding what becomes of one there.
package queue

import "context"

// Rejecter dead-letters a leased message at once, with the cause that
// condemned it, whatever its delivery count.
//
// It is the broker's half of a handler saying "do not retry" — [DoNotRetry]
// is the handler's — and internal/service/data/queue.Consume joins the two: a
// handler whose failure carries [CodeNotRetryable] is rejected rather than
// nacked, so a payload that will never decode reaches the dead-letter store
// on its first failure instead of its MaxDeliveries-th, and an operator sees
// it one lease and no retry delay later.
//
// It is a sibling of [Broker] for the reason [DeadLetterReader] is: a
// connector onto a system with no in-band dead-letter move cannot implement
// it, and the engine falls back to Nack for such a broker — the message is
// then retried until MaxDeliveries and dead-lettered with the same cause, so
// the shortcut is lost and never the message. The three brokers in
// internal/service/data/queue implement it.
type Rejecter interface {
	// Reject moves the leased message named by receipt to the dead-letter
	// store with cause, recorded as [Broker.Nack] records the cause of a
	// message that ran out of attempts: its Reason, its dotted-quad Code and
	// its wire-safe Public half, never the Private one. The dead letter's
	// Deliveries is the count the message had, which may be below
	// [PolicyValue.MaxDeliveries].
	//
	// A lapsed receipt is [LeaseExpired] and one this broker cannot read is
	// [UnknownReceipt], exactly as [Broker.Ack] refuses them; neither moves
	// anything.
	Reject(ctx context.Context, receipt ReceiptValue, cause error) error
}

// DeadLetterManager puts a dead letter back into its queue, or deletes it.
//
// Reading the store ([DeadLetterReader]) is evidence and never changes it;
// these two calls are the decisions an operator takes after reading it — the
// failure was fixed downstream, so the message is replayed; the message is
// obsolete, so it is deleted. They take the dead letter's [MessageValue].ID,
// the identifier DeadLetters returned and the failing consumer logged.
//
// A sibling of [Broker] for the reason [DeadLetterReader] is. The three
// brokers in internal/service/data/queue implement it.
//
//	if manager, ok := broker.(queue.DeadLetterManager); ok {
//		err := manager.ReplayDeadLetter(ctx, dead.Message.ID)
//	}
type DeadLetterManager interface {
	// ReplayDeadLetter moves the dead letter id back into the queue, visible
	// at once, with its delivery count reset: its next delivery counts 1 and
	// it gets [PolicyValue.MaxDeliveries] attempts again. It keeps its ID, its
	// payload and its EnqueuedAt, so the log lines of both its lives join on
	// one identifier; the dead-letter record, its cause included, is gone once
	// the message is queued again — log it first if it matters.
	//
	// An id the store does not hold is [DeadLetterNotFound], and nothing is
	// queued. Two replays of one dead letter racing each other may both
	// queue it on a broker that cannot tell them apart, which is a duplicate
	// the at-least-once guarantee already permits; the brokers say which
	// they are.
	ReplayDeadLetter(ctx context.Context, id string) error
	// DeleteDeadLetter removes the dead letter id for good, payload, cause
	// and all. It is the only call in the domain that destroys a message
	// without its consumer's acknowledgement, and it is never made by the
	// SDK itself. An id the store does not hold is [DeadLetterNotFound].
	DeleteDeadLetter(ctx context.Context, id string) error
}
