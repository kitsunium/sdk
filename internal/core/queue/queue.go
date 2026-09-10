// Package queue declares the SDK's ASYNCHRONOUS, DURABLE message queue port:
// the [Broker] a producer publishes into and a consumer leases from, the
// [Handler] that processes one delivery, and the values that carry a message,
// its lease and its eventual dead letter. A core sibling admitted by ADR 0054.
//
// # queue is not events, and the line was drawn before this package existed
//
// ADR 0053 §D1 wrote the frontier down while queue was still hypothetical,
// precisely so it could not be renegotiated by whoever wrote it. This package
// is the right-hand column, on every axis:
//
//	                     | events                   | queue (this package)
//	---------------------|--------------------------|--------------------------
//	Scope                | one process              | many processes
//	Timing               | synchronous              | asynchronous
//	Goroutine            | the publisher's          | a consumer's
//	Transaction          | the publisher's          | its own
//	Durability           | none                     | the point of it
//	Retries / DLQ        | none                     | the point of it
//	Process dies in      | the event never happened | the message is still
//	flight               |                          | there
//
// A caller who wants "several things happen right now, on my goroutine,
// inside my transaction" wants internal/core/events. This package will not
// give them a faster version of it: [Broker.Publish] costs a disk flush,
// because that flush IS the guarantee.
//
// # The delivery guarantee is AT-LEAST-ONCE, and the type says so
//
// Exactly-once delivery does not exist over a transport. What exists is
// at-least-once delivery plus an idempotent consumer, and every system that
// advertises the first is selling the second with the consumer's half left as
// an exercise. This port therefore promises at-least-once and puts the
// consequence in the signature rather than in a paragraph:
// [DeliveryValue.Deliveries] is a field of every delivery, it counts from 1,
// and a value greater than 1 means this consumer — or another one, in another
// process — has already seen these bytes.
//
// A message is removed at ACKNOWLEDGEMENT, never at read. [Broker.Receive]
// LEASES a message: it becomes invisible to every other consumer until
// [PolicyValue.VisibilityTimeout] elapses. If the consumer acknowledges, the
// message is gone. If the consumer dies — SIGKILL, a power cut, an OOM — it
// never acknowledges, the lease lapses, and the message is delivered again
// with Deliveries incremented. That is the entire mechanism, and it is why
// the guarantee is at-least-once and not at-most-once.
//
// # What this port does NOT guarantee
//
//   - **Exactly-once.** See above. Make the handler idempotent; the SDK's
//     consumer engine (internal/service/queue.Consume) makes you assert that
//     in code.
//   - **Order under retry.** A queue with one consumer and no failures
//     delivers in publication order. A retried message becomes visible again
//     LATER than messages published after it, so a single failure reorders
//     the stream — and more than one consumer removes ordering entirely.
//     There is no per-key ordering here; see the ADR's Deferred section.
//   - **Durability beyond what fsync actually does.** An implementation that
//     flushes has done everything a process can do; a lying disk cache, a
//     virtualised host that acknowledges early, or a filesystem mounted with
//     barriers off will still lose an acknowledged write, and no library can
//     detect that.
//
// # Layers
//
// The two concrete brokers — one in memory, one on the filesystem — the
// consumer engine, and the dead-letter record live in
// internal/service/queue. This package owns the contract, the domain values,
// the policy and its guard, and the typed sentinels a refusal carries.
package queue

import (
	"context"
	"time"
)

// Broker is one named queue: producers publish into it, consumers lease from
// it, and it — not the consumer — owns the delivery count and the decision to
// dead-letter. Implementations MUST be safe for concurrent use by multiple
// goroutines AND, where the implementation is durable, by multiple processes.
//
// IFACE-PLUGIN: the concrete brokers stay unexported behind their
// constructors in internal/service/queue.
//
// The interface is FROZEN at these four methods. pkg/v1/queue aliases it, Go
// interfaces are structural, and a fifth method would break every downstream
// implementation at compile time with no deprecation window (ADR 0039). A new
// capability arrives as a sibling interface reached by type assertion —
// [DeadLetterReader] and [LeaseExtender] are the two that ship.
//
// # Why the BROKER owns the delivery count
//
// Because the consumer is the thing that dies. A count held by the consumer
// engine is lost by exactly the event the count exists to survive, so a
// process that crashes on every third message would retry forever and never
// dead-letter. The count travels with the message, in whatever the
// implementation's durable representation is, and the broker compares it
// against [PolicyValue.MaxDeliveries] on every [Broker.Nack] and on every
// lapsed lease.
type Broker interface {
	// Publish appends payload to the queue and returns the message the
	// broker minted for it.
	//
	// It takes BYTES and not a [MessageValue] on purpose: the identifier and
	// the enqueue instant are the broker's to assign, and a struct with
	// fields the caller must leave blank is a struct that will eventually be
	// filled in.
	//
	// When it returns nil, a durable implementation has flushed the message
	// to its device: a crash one instruction later does not lose it. That
	// flush is most of what the call costs — see the domain's BENCH.md.
	//
	// A payload larger than [PolicyValue.MaxMessageBytes] is
	// [MessageTooLarge]; an empty payload is legitimate, because a message
	// whose whole meaning is its arrival is a normal thing to send.
	Publish(ctx context.Context, payload []byte) (MessageValue, error)
	// Receive leases up to max messages and returns them. It does NOT block
	// waiting for work: an empty queue yields an empty slice and a nil
	// error, and polling is the caller's (see internal/service/queue.Consume,
	// which does it for you).
	//
	// Each returned message is invisible to every other consumer until its
	// [DeliveryValue.Lease] expires. A consumer that finishes acknowledges
	// with [Broker.Ack]; a consumer that fails reports it with [Broker.Nack];
	// a consumer that DIES does neither, and the lapsed lease is what makes
	// the message reappear.
	//
	// max must be positive: [InvalidBatchSize] otherwise, rather than an
	// empty slice forever from a consumer loop that spins and processes
	// nothing.
	Receive(ctx context.Context, max int) ([]DeliveryValue, error)
	// Ack removes the leased message permanently. It is the only call that
	// does.
	//
	// A receipt whose lease has already lapsed is [LeaseExpired] and removes
	// NOTHING: the message is back in the queue, or in the dead-letter
	// store, and another consumer may already hold it. Reporting that is the
	// same decision core/lock makes for a lapsed lease — a holder that lost
	// its claim must not be able to end somebody else's turn, and must find
	// out that it lost it.
	//
	// A receipt this broker never issued, or one it cannot parse, is
	// [UnknownReceipt].
	Ack(ctx context.Context, receipt ReceiptValue) error
	// Nack reports that processing failed and hands the message back.
	//
	// The broker decides what happens next and reports it: the message
	// becomes visible again after [PolicyValue.RetryDelay], or — when it has
	// now been delivered [PolicyValue.MaxDeliveries] times — it is moved to
	// the dead-letter store together with cause. See [NackValue].
	//
	// cause is recorded. What is kept of it is the implementation's to
	// document, and the rule this domain follows is CLAUDE.md rule 4: the
	// wire-safe Public half and the dotted-quad code travel with the dead
	// letter, the Private half stays in the failing process's log, and the
	// message identifier is the join key between them. A nil cause is
	// accepted and recorded as an unexplained failure, because a consumer
	// that knows only "this did not work" must still be able to say so.
	//
	// A lapsed or unknown receipt is refused exactly as [Broker.Ack] refuses
	// it.
	Nack(ctx context.Context, receipt ReceiptValue, cause error) (NackValue, error)
}

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
// [PolicyValue.MaxDeliveries]. ctx is the consumer's and IS honoured: unlike
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
	// with its cause. It will not be delivered again.
	DeadLettered bool
}
