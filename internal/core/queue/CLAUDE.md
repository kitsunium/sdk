# internal/core/queue/

## Purpose

The SDK's ASYNCHRONOUS, DURABLE message queue port — ADR 0054. The 18th core
sibling, and the right-hand column of the frontier table ADR 0053 §D1 wrote
down before this domain existed.

|  | `events` (ADR 0053) | `queue` (this domain) |
|---|---|---|
| Scope | one process | many processes |
| Timing | synchronous | asynchronous |
| Goroutine | the publisher's | a consumer's |
| Transaction | the publisher's | its own |
| Durability | none | the point of it |
| Retries / DLQ | none | the point of it |
| Process dies in flight | the event never happened | the message is still there |

That table is not a comparison, it is a contract between two packages. A change
here that moves this domain towards the left-hand column is wrong by
construction; a change to `events` that moves it right is wrong the same way.

## Contents

| File | Holds |
|---|---|
| `queue.go` | package doc (the frontier, the guarantee, the three non-guarantees), `Broker` (FROZEN at four methods), `Handler` (FUNC port), `NackValue` |
| `message.go` | `MessageValue`, `ReceiptValue`, `LeaseValue`, `DeliveryValue` |
| `capability.go` | the ADR 0039 siblings — `DeadLetterReader`, `LeaseExtender` — and `DeadLetterValue` |
| `policy.go` | `PolicyValue`, `Validate`, `Normalized`, `DefaultMaxMessageBytes` |
| `codes.go` | the five `0.2.23.*` codes |
| `errors.go` | the five sentinels |

## The five things this domain decided

1. **At-least-once.** Exactly-once does not exist over a transport. The
   consequence is in the TYPE, not in a paragraph: `DeliveryValue.Deliveries`
   is a field of every delivery and counts from 1, so nothing can read a
   delivery without reading the number that says this may not be the first
   time. The push side adds a second, harder statement —
   `service/queue.ConsumerConfig.HandlerIsIdempotent`, refused at false.
2. **Removed at ACKNOWLEDGEMENT, never at read.** `Receive` leases; `Ack` is
   the only call in the domain that removes a message. A consumer that dies
   between the two never acknowledges, its lease lapses, and the message is
   delivered again.
3. **Dead-lettered after `MaxDeliveries`, WITH the cause.** `DeadLetterValue`
   keeps the failure's `Reason`, its dotted-quad `Code` and its wire-safe
   `Cause` (the Public half). The Private half is not kept — a dead-letter
   store is read by whoever is investigating, often not the process or the
   trust domain that failed — and `MessageValue.ID` is the join key back to the
   log line that has it.
4. **Ordering is not guaranteed.** FIFO holds for one consumer with no
   failures. A single retry breaks it, because a nacked message becomes visible
   again after the messages published behind it. There is no per-key ordering.
5. **Zero values, both branches of ADR 0031, in one struct.**
   `VisibilityTimeout` and `MaxDeliveries` are REFUSED at zero (two opposite
   readings each, one of which silently destroys the guarantee — `core/lock`'s
   TTL argument); `RetryDelay` and `MaxMessageBytes` are CLAMPED (one reading
   each, harmless).

## Conventions

- **Frozen ports.** `Broker` has four methods and gets no fifth. `Handler` is a
  func type, so it cannot grow one at all. A capability is a sibling interface
  reached by type assertion.
- **No registry.** One `Broker` value is one queue; the name of the queue is
  the implementation's configuration (a directory, for the file broker). A
  registry would have one entry per queue and would add a way to misconfigure a
  wiring at runtime.
- **The routing key is a NAME, and it has to be.** `events` routes on
  `reflect.Type` because the compiler mints it and it cannot collide. That
  argument is process-local: a Go type has no identity in another process, so a
  domain whose whole point is crossing that boundary cannot use one. This is a
  direct consequence of the frontier, not a weaker choice.
- **No payload in any error.** Every sentinel's Public says what went wrong and
  names sizes, fields and operations — never bytes. Pinned by
  `TestAnOversizedPayloadIsRefusedAtTheProducer`, which publishes a recognisable
  secret and greps the rendered error for it.

## Do NOT

- Add a fifth method to `Broker`, or a method to `Handler`. Both are published
  through `pkg/v1/queue` aliases; a sibling is the only extension (ADR 0039).
- Add an "exactly-once" mode, a "delivery guarantee" enum, or a
  `Config.Ordered`. The first does not exist, and the other two would make the
  guarantee a construction-time variable that no call site can be read against
  — ADR 0053 §D1's mistake, one domain over.
- Put a broker, a goroutine, a timer or an I/O handle in this package. It owns
  the contract, the values, the policy guard and the sentinels.
- Widen `PolicyValue` with a knob whose zero is inert. Every field here either
  refuses or clamps, and which one it does is argued in its doc comment.
- Make `ReceiptValue` parseable, comparable to a `MessageValue.ID`, or
  constructible by a caller. Two deliveries of one message carry two receipts;
  that is what lets a broker refuse an acknowledgement from a lapsed lease.

## Reference

- ADR 0054 — `docs/adr/0054-sdk-queue-domain.md`
- ADR 0053 §D1 — the frontier table, written before this domain existed
- ADR 0031 — a zero value is a safe default or an explicit refusal
- ADR 0039 — a published port grows by siblings, never by widening
- `internal/service/queue/CLAUDE.md` — the two brokers and the consumer engine
- `internal/service/queue/BENCH.md` — what durability costs, measured
