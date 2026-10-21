# ADR 0183 — a message may wait for its instant, and recurrence stays the scheduler's

- **Status**: Accepted
- **Date**: 2026-10-21
- **Accepted**: 2026-10-21, decided autonomously by the SDK maintainers for
  kitsunium/sdk#481. It can be revisited while the SDK is v0
  ([ADR 0040](0040-changing-a-published-shape-while-v0.md)): every symbol it
  adds is a sibling a later decision may change or withdraw.
- **Deciders**: SDK maintainers
- **Resolves**: [ADR 0054](0054-sdk-queue-domain.md) Deferred, "Delayed/scheduled
  publication (`PublishAt`)"
- **Extends**: [ADR 0054](0054-sdk-queue-domain.md) and
  [ADR 0151](0151-a-message-published-in-a-transaction-exists-if-and-only-if-it-commits.md),
  by three siblings per [ADR 0039](0039-extending-a-published-port-without-breaking-it.md)
- **Related**: [ADR 0041](0041-sdk-scheduler-domain.md) (the scheduler),
  [ADR 0031](0031-policy-zero-values-are-never-inert.md) (zero values),
  [ADR 0090](0090-a-port-named-in-public-must-be-implementable-in-public.md) and
  [ADR 0103](0103-a-bucket-per-caller-one-backoff-curve-and-a-retry-on-the-clock-it-is-given.md)
  (the injected clock), kitsunium/sdk#481

## Context

ADR 0054 left `PublishAt` deferred on purpose. The mechanism was already there
— every broker records when a message becomes visible, which is how a retry
delay works — but the decision was not: should a queue also be a scheduler?
ADR 0041 is where scheduling lives, and it says the scheduler keeps no state
across a restart.

Three gaps against the state of the art (kitsunium/sdk#481) now make the
question concrete:

- **Work that falls due once, later.** A trial that ends in fourteen days, an
  order cancelled if still unpaid in an hour, a reminder at nine. SQS has
  `DelaySeconds` (fifteen minutes at most), River `ScheduledAt`, Sidekiq
  `perform_at`. The scheduler cannot hold it: a process that restarts in the
  meantime forgets it.
- **A retry whose moment the handler knows.** A downstream that answered 429
  with a `Retry-After`, a mail server that greylisted the sender. JetStream has
  a delayed negative acknowledgement and SQS `ChangeMessageVisibility`. The
  SDK's mail spool fakes one today by extending its own lease.
- **A producer that retries a publication.** A timeout, or a crash between the
  call and the log line, and the same work is enqueued twice. JetStream
  deduplicates on `Nats-Msg-Id` over two minutes, SQS FIFO over five, River
  with unique jobs.

## Decision

### D1 — The frontier with the scheduler: one instant in the queue, recurrence in the scheduler

A queue may hold a message back until **one** instant its producer names, and
it keeps that instant as durably as the message. Recurrence — anything that
fires again on a calendar — stays the scheduler's (ADR 0041).

| | `scheduler` (ADR 0041) | `queue` with `PublishAt` (this ADR) |
|---|---|---|
| What is scheduled | a job, again and again | one message, once |
| State across a restart | none, by design | the message, durable |
| Several processes | each fires every job | one consumer takes the message |
| Missed while down | dropped, and counted | delivered when a consumer looks |
| Delivery | in this process, on time | at least once, at or after the instant |

The two meet in one direction only: a scheduled job may publish into a queue
(now, or for later). A queue never re-publishes a message on a calendar, and
no "every Monday" arrives in this domain. That keeps both rows of the table
honest: the scheduler stays stateless and the queue does not grow a calendar,
a timezone or a missed-fire policy.

### D2 — `DelayedPublisher.PublishAt(ctx, payload, at)`

A sibling of `Broker`, not a fifth method (ADR 0039). The message's
`EnqueuedAt` is the broker's now; `at` is its visibility, reported by
`MessageReader` as `VisibleAt`. An `at` already past — the zero `time.Time`
included — has the one reading "now" (ADR 0031). An `at` past what a durable
broker records (2262, Unix nanoseconds) is `QUEUE_MISCONFIGURED` and nothing
is published. Everything else is `Publish`'s: durable when it returns nil, part
of the caller's transaction on the SQL broker (ADR 0151), consumers woken and
then sleeping until the instant (ADR 0104). There is no ceiling below 2262:
SQS's fifteen minutes is a property of its storage, not of the idea.

Each broker writes its normal message with a later visibility: the memory
broker's ready list, the file broker's queued name (whose first field is the
visibility instant), the SQL broker's `due` column. No new state, no new
statement.

### D3 — `DelayedNacker.NackAfter(ctx, receipt, cause, delay)` and `RetryAfter(cause, delay)`

A sibling of `Broker`. It is `Nack` with a delay its caller names instead of
the policy's `RetryDelay`/`MaxRetryDelay`; the delivery is counted, and on the
last attempt the message is dead-lettered with `cause`, the delay unused. A
negative delay reads as zero, "now". A delay whose instant no durable broker
records is `QUEUE_MISCONFIGURED`, and the lease stays valid. The signature
carries `cause` because a nack may dead-letter, and a dead letter keeps its
cause (ADR 0054 D3).

`RetryAfter(cause, delay)` is the handler's half, built like `DoNotRetry`
(ADR 0151): an `errs.Wrap` onto `cause`, so an SDK cause stays the origin a
dead letter records, `CodeRetryRequested` (`0.2.23.10`) joins its wrap trail,
and the delay rides as a field; a foreign or nil cause records the new sentinel
`RETRY_REQUESTED` (`EX_TEMPFAIL`). `RetryAfterOf(err)` reads the delay back. The
delay is held to `[0, MaxDeadlineOffset]`. `Consume` reads the mark after
`DoNotRetry` and after a stop: a failure marked both ways is rejected, and one a
stop cut short is handed back uncounted. A broker without the sibling is
nacked: the message waits the policy's delay, so the delay is lost and never
the message.

### D4 — `OncePublisher.PublishOnce(ctx, key, payload, window) (MessageValue, bool, error)`

A sibling of `Broker`. It publishes and reserves `key` for `window`, unless the
key is still reserved: then it publishes nothing and returns `false` with the
zero `MessageValue`.

**A duplicate is a boolean, not a sentinel.** It is the expected outcome of the
retry the call exists for. A caller asking "did it go out?" should not have to
tell a dedicated error from a storage failure, and code that only checks
`err != nil` keeps working. The first publication's message is not returned:
the SQL broker could not read it back past a REPEATABLE READ snapshot on MySQL,
and the key is what the caller holds.

An empty key is refused, and so is a non-positive window: "never deduplicate"
and "forever" are opposite readings of zero (ADR 0031). A window whose end no
durable broker records is refused too. All three are `QUEUE_MISCONFIGURED`,
and nothing is published. The window is the broker's clock from the
publication that reserved the key, and is independent of the message: an
acknowledged message still holds its key until the window ends. Passed
reservations are removed lazily, by a later `PublishOnce`. Keys are compared
byte for byte; the durable brokers keep only their SHA-256.

It narrows duplicates at the producer and does not remove them. The queue
still delivers at least once, and `HandlerIsIdempotent` is still required.

How each broker keeps the reservation:

- **Memory**: a map from key to the end of its window, and a heap of those ends
  that drops passed ones without a scan.
- **SQL**: a row of the queue's own table, `dead = 2`, its id the key's SHA-256
  (64 hex bytes, never a message identifier, which has a dash), its `due` the
  window's end. The primary key is the unique constraint, so **no migration**:
  a table `SQLMigration` created before this ADR serves it as it is, and every
  existing statement names `dead = 0` or `dead = 1` and never reads such a row.
  The call is one transaction (the caller's, as a savepoint): drop the key's
  passed reservation, claim the key with an insertion that affects no row when
  it is taken (`ON CONFLICT (id) DO NOTHING`; MySQL `INSERT IGNORE`, whose
  other effect, a silent truncation, cannot occur on a digest and integers),
  insert the message only when the claim took, and remove up to 100 passed
  reservations of other keys, read `FOR UPDATE SKIP LOCKED`. The reservation
  and the message commit together or not at all: no duplicate, no loss.
- **File**: `once/<sha256>.key`, a fourth directory held to the state
  directories' rule (an account that could plant a reservation could silence a
  key). The reservation is written under a name of its own created with
  `O_EXCL`, then `link(2)`ed to the key's name. The link is exclusive as
  `O_EXCL` is, and the name never appears without its end. The message is
  written **first** and the key reserved after. So a crash between the two
  costs a duplicate, never a lost message, and the other order would answer
  "already published" for a message nobody wrote. A plain duplicate is read
  off the reservation before anything is written. Only producers racing in the
  same instant write a message and then unlink it before anybody is woken. A
  passed reservation is renamed aside, read again and only then removed, so a
  takeover never removes the one another producer just made. The directory is
  swept at most once a minute per broker.

## Consequences

- `Broker` stays at four methods. `TestTheQueueSiblingsKeepTheirMethodCounts`
  pins each new sibling at one. The three brokers implement all three
  siblings, and the conformance suite runs the same cases on all five
  fixtures (memory, file, SQL on each dialect's statements), on a
  `ManualClock`.
- `e2e/integration/sql` runs the deferred publication, the delayed nack and
  sixteen producers racing on one key on SQLite, PostgreSQL and MySQL.
- The mail spool, which parked a mail by extending its own lease, parks it with
  `NackAfter` where the broker has it. The mail comes back at the same instant
  with the same count, and during its backoff it is a waiting message rather
  than a leased one.
- New code `0.2.23.10` (`RETRY_REQUESTED`), in the range ADR 0054 allocated.

## Alternatives considered

- **`PublishAt` as a scheduler feature** (a persistent scheduler). Rejected: it
  would give ADR 0041 a store, a schema and the multi-replica question it
  deliberately does not answer. The queue already has all three.
- **A recurring message** (`PublishEvery`). Rejected by D1: recurrence needs a
  calendar, a timezone and a missed-fire policy, which are the scheduler's.
- **A `NackOptions` widening of `Nack`.** Rejected: `Broker` is frozen
  (ADR 0039).
- **A sentinel for a duplicate** (`errors.Is(err, queue.Duplicate)`). Rejected
  by D4: it turns the success of a retry into an error path.
- **A dedicated SQL table and a second migration for the reservations.**
  Rejected: a table that predates the sibling would fail `PublishOnce` at
  runtime until the caller added a migration. The queue table's primary key
  already is the unique constraint.
- **The file reservation before the message.** Rejected: a crash between the
  two would hold the key with no message, a loss reported as a success.

## Deferred

- **`PublishOnce` at an instant** (a deduplicated deferred message). It is the
  framework's `Command.DispatchAt` with a unique key. It needs either a fourth
  sibling or a value carrying both, and the framework's first use should
  decide which.
- **The first publication's identifier on a duplicate.** See D4.
- **The framework and the Studio**: `app.RetryIn`, a deduplication key declared
  on a topic, `Command.DispatchAt`, jobs scheduled for later shown with their
  instant (kitsunium/sdk#481, framework and Studio stages).

## References

- [ADR 0054](0054-sdk-queue-domain.md) (the domain, and the deferral this
  resolves), [ADR 0151](0151-a-message-published-in-a-transaction-exists-if-and-only-if-it-commits.md)
  (the SQL broker, `DoNotRetry`), [ADR 0041](0041-sdk-scheduler-domain.md)
  (the scheduler), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md)
  (siblings), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (zero
  values), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (v0).
- Impl: `internal/core/data/queue` (the three ports, `RetryAfter`,
  `RetryAfterOf`, `RETRY_REQUESTED`), `internal/service/data/queue`
  (`memory.go`, `file.go`, `file_dead.go`, `file_once.go`, `sql.go`,
  `sql_once.go`, `consume.go`), `pkg/v1/data/queue`.
