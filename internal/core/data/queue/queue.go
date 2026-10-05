package queue

import (
	"context"
	"time"
)

// Handler processes one delivery. It is the port a consumer implements, and
// it is a FUNCTION type rather than an interface — the shape
// internal/core/CLAUDE.md already admits for resilience.Operation,
// scheduler.Job, lifecycle.Start and events.Listener. ADR 0039's rule is that
// a published port must not grow a method; a func type satisfies it
// structurally, because it cannot grow one at all.
//
// It receives the whole [DeliveryValue] and not just the payload, so that
// [DeliveryValue.Deliveries] is in front of whoever writes the handler. A
// handler WILL be called more than once for the same message — that is the
// guarantee, not a malfunction — and the count is the only thing that lets it
// say so in a log.
//
// Returning nil acknowledges. Returning an error nacks: the message is
// retried, or dead-lettered once it has exhausted
// [PolicyValue.MaxDeliveries]. Returning [DoNotRetry](err) says no retry can
// fix it, and the message is dead-lettered at once, with err, through a
// broker's [Rejecter]. ctx is the consumer's and IS honoured: unlike
// events.Listener, a handler that outlives its context is holding a lease it
// is about to lose, so abandoning the work is the correct response and the
// message will simply be redelivered.
type Handler func(ctx context.Context, delivery DeliveryValue) error

// NackValue is what [Broker.Nack] decided.
//
// It exists because "I reported the failure" and "that message is now dead"
// are different facts, and a consumer that cannot tell them apart cannot log
// the one that matters. Every field is a scalar: a nack is on the failure
// path, but it is on the failure path of a queue that is expected to retry,
// so it is not rare enough to allocate on.
type NackValue struct {
	// Deliveries is how many times the message had been delivered when it
	// failed, counting from 1.
	Deliveries int
	// VisibleAt is when the message becomes available again. It is the zero
	// Time when DeadLettered is true.
	VisibleAt time.Time
	// DeadLettered reports that the message exhausted
	// [PolicyValue.MaxDeliveries] and was moved to the dead-letter store
	// with its cause. It will not be delivered again, unless somebody replays
	// it ([DeadLetterManager]).
	DeadLettered bool
}
