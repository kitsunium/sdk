<!-- updated: 2026-10-02T19:53:04Z -->
# internal/core/queue/

## Purpose

The SDK's ASYNCHRONOUS, DURABLE message queue port — ADR 0054. The 21st core
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
| `queue.go` | package doc (the frontier, why a publication may join the publisher's transaction while the handler never does, the guarantee, the three non-guarantees), `Broker` (FROZEN at four methods), `Handler` (FUNC port), `NackValue` |
| `message.go` | `MessageValue`, `ReceiptValue`, `LeaseValue`, `DeliveryValue` |
| `capability.go` | the ADR 0039 siblings `DeadLetterReader` and `LeaseExtender`, and `DeadLetterValue` |
| `wake.go` | the third sibling, `Waker`, and the `WakeValue` it answers with (ADR 0104) |
| `deadletter.go` | the fourth and fifth siblings (ADR 0151): `Rejecter` — dead-letter a leased message at once — and `DeadLetterManager` — `ReplayDeadLetter` and `DeleteDeadLetter` |
| `retry.go` | `DoNotRetry(cause)`: the mark a handler puts on a failure no retry can fix (ADR 0151) |
| `policy.go` | `PolicyValue` (with `MaxRetryDelay`, ADR 0151), `Validate` + `validateRetryGrowth`, `Normalized`, `DefaultMaxMessageBytes`, `MaxDeadlineOffset` |
| `codes.go` | the seven `0.2.23.*` codes |
| `errors.go` | the seven sentinels |

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
   each, harmless). The two durations are also bounded from ABOVE, and that
   refusal is arithmetic, not a reading: above `MaxDeadlineOffset` (a century)
   they are REFUSED. A deadline is now plus one of them, the durable broker
   writes it into a filename as int64 Unix nanoseconds, and that range ends on
   2262-04-11 — `math.MaxInt64`, "never", wrapped the deadline negative, the
   name could not be read back, and the message was stranded in `inflight/`
   with a receipt reading `UNKNOWN_RECEIPT`. `Validate` has no clock, so the
   ceiling is fixed; it keeps every deadline representable for any clock
   before 2162, and a negative `RetryDelay` is still "no delay". Pinned by
   `TestPolicyRefusesADeadlineOffsetPastTheCeiling` here and, on every broker,
   by `service/queue`'s `TestEveryBrokerRefusesADeadlineOffsetNoInstantCanCarry`.

## What ADR 0151 added, and why each piece is where it is

- **`MaxRetryDelay` makes the retry delay grow.** Positive, the message nacked
  on its n-th delivery waits `RetryDelay × 2^(n−1)`, held at the ceiling —
  `resilience.Backoff`'s curve (ADR 0103), which the brokers call; this package
  cannot import it and only holds the numbers. Zero keeps the constant delay
  every policy had, which is the zero's one reading and what keeps the field
  backward compatible. A ceiling that is no ceiling is refused by field and
  problem: negative, past `MaxDeadlineOffset`, above no `RetryDelay` (growth
  from zero is zero forever, an inert knob), or below `RetryDelay` (it would
  silently shorten every wait). `TestAGrowingRetryDelayNeedsARealCeiling`.
- **`DoNotRetry(cause)` is the handler's half of immediate dead-lettering.** It
  is an `errs.Wrap` onto the cause with `CodeNotRetryable` as the wrap-site
  code, so ORIGIN WINS: an SDK cause keeps its own Reason, Code and Public — the
  three things a dead letter records, D10 of ADR 0054 — and the mark rides the
  wrap trail, where `errs.HasCode` finds it. A stdlib cause, or none, records
  `NOT_RETRYABLE` itself. `errors.Is(err, NotRetryable)` sees only the origin,
  which is why the recognition is by code and the doc says so.
  `TestDoNotRetryMarksTheFailureAndKeepsTheCauseItsOrigin`.
- **`Rejecter` is the broker's half**, a sibling because `Broker` is frozen and
  a connector with no in-band dead-letter move cannot implement it — the engine
  falls back to `Nack` then, which loses the shortcut and never the message.
  It lives in core because a type the port speaks lives in core (ADR 0074).
- **`DeadLetterManager` replays or deletes one dead letter by `MessageValue.ID`.**
  Reading the store stays evidence that no read consumes; these two are the
  decisions an operator takes after it. A replay keeps the ID, the payload and
  the enqueue instant, resets the count to zero and loses the cause record;
  an ID the store does not hold is `DEAD_LETTER_NOT_FOUND` (404, `EX_NOINPUT`).
- **`TestTheQueueSiblingsKeepTheirMethodCounts`** pins `Broker` at four methods
  and each sibling at the count it shipped with.

## Conventions

- **Frozen ports.** `Broker` has four methods and gets no fifth. `Handler` is a
  func type, so it cannot grow one at all. A capability is a sibling interface
  reached by type assertion: `DeadLetterReader`, `LeaseExtender`, `Waker`,
  `Rejecter`, `DeadLetterManager` — each frozen at the methods it shipped with.
- **A publication may join the publisher's transaction; the handler never
  does.** The frontier's Transaction row is about where the WORK runs, and it
  is untouched by the SQL broker (ADR 0151): its `Publish` makes the MESSAGE
  part of the caller's transaction — the transactional outbox — while the
  handler still runs later, on a consumer's goroutine, in its own.
- **`Waker` is a hint, and says so (ADR 0104).** `Wake()` hands out a signal
  closed by the next Publish or Nack IN THIS PROCESS, and how long until
  something the broker holds becomes receivable on its own. A wake may find
  nothing and work may arrive with no wake — another process's publication —
  so a consumer that uses it still polls, at a cadence that bounds the second
  case. `WakeValue.In` is a duration on the broker's clock, never an instant,
  so a consumer on a different clock waits the right length instead of
  comparing two clocks.
- **No registry.** One `Broker` value is one queue; the name of the queue is
  the implementation's configuration (a directory for the file broker, a
  database and a table for the SQL one). A
  registry would have one entry per queue and would add a way to misconfigure a
  wiring at runtime.
- **The routing key is a NAME, and it has to be.** `events` routes on
  `reflect.Type` because the compiler mints it and it cannot collide. That
  argument is process-local: a Go type has no identity in another process, so a
  domain whose whole point is crossing that boundary cannot use one. This is a
  direct consequence of the frontier, not a weaker choice.
- **No payload in any error.** Every sentinel's Public says what went wrong and
  names sizes, fields and operations — never bytes. Pinned by
  `service/queue`'s `TestAnOversizedPayloadIsRefusedAtTheProducer`, which publishes a recognisable
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
- ADR 0151 — the SQL broker, a growing retry delay, immediate dead-lettering, dead-letter replay and deletion
- `internal/service/queue/CLAUDE.md` — the three brokers and the consumer engine
- `internal/service/queue/BENCH.md` — what durability costs, measured
