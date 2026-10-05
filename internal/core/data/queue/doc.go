// Package queue — the ADR 0039 capability siblings of the frozen [Broker]
// port, and the dead-letter record one of them returns.
//
// Package queue — range 0.2.23.* (ADR 0054 core/data/queue block), and the
// brokers' and the consumer engine's 0.3.53.* (ADR 0054 service/data/queue
// block, declared here since ADR 0160).
//
// Package queue — the two ADR 0039 siblings that act on the dead-letter store:
// sending a message there at once, and deciding what becomes of one there.
//
// Package queue — declares the sentinel *errs.Error port outcomes, and the
// implementation outcomes of the brokers and the consumer engine in
// internal/service/data/queue (ADR 0160: every code is declared in the core,
// at the service's path). Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form.
//
// Package queue — hosts the three values that travel between a producer, a
// broker and a consumer: the message, the lease, and the delivery that pairs
// them.
//
// Package queue — hosts the delivery policy every broker is configured with,
// and the guard both implementations run so that the two refuse identical
// inputs identically.
//
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
// inside my transaction" wants internal/core/app/events. This package will not
// give them a faster version of it: [Broker.Publish] costs a disk flush,
// because that flush IS the guarantee.
//
// # A publication may join the publisher's transaction; the handler never does
//
// The Transaction row is about where the WORK runs, and a handler always runs
// in a transaction of its own, on a consumer's goroutine. What a broker over
// the publisher's own database may do is make the MESSAGE part of the
// publisher's transaction (ADR 0151): published inside it, the message exists
// if and only if that transaction commits — the transactional outbox, and the
// only way a message and the write that caused it are never one without the
// other. That moves no row of the table: the handler still runs later,
// elsewhere, and in its own transaction.
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
//     consumer engine (internal/service/data/queue.Consume) makes you assert that
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
// The three concrete brokers — one in memory, one on the filesystem, one in a
// SQL database — the consumer engine, and the dead-letter record live in
// internal/service/data/queue. This package owns the contract, the domain values,
// the policy and its guard, and the typed sentinels a refusal carries.
//
// Package queue — the failure a handler reports when no retry can fix it.
//
// Package queue — the ADR 0039 sibling that lets a consumer sleep until there
// may be work, and the value it answers with (ADR 0104).
package queue
