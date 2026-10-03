# ADR 0151 — a message published in a transaction exists if and only if it commits

- **Status**: Accepted
- **Date**: 2026-09-30
- **Deciders**: SDK maintainers
- **Amended by**: [ADR 0159](0159-the-kernel-holds-what-the-domains-rewrote-and-is-published-by-nature.md) §Consequences — §D6: the driver's withheld text is one `Withheld` in `service/data/sql`, shared by the queue's and the document store's SQL engines, not a copy per package
- **Amends**: [ADR 0054](0054-sdk-queue-domain.md) (a third broker, two capability siblings, a policy field; the frontier table is kept and its Transaction row is read precisely), [ADR 0104](0104-an-idle-consumer-sleeps-until-there-may-be-work.md) (the wake table keyed by queue, not by directory)
- **Related**: [ADR 0139](0139-a-document-store-over-sql-joins-the-transaction-its-context-carries.md) (the `Joiner`, the `Deferrer`, and the transaction rules this broker follows), [ADR 0140](0140-sqlites-migration-lock-is-the-database-files-write-lock.md) (a write that writes nothing takes SQLite's lock), [ADR 0103](0103-a-bucket-per-caller-one-backoff-curve-and-a-retry-on-the-clock-it-is-given.md) (the one backoff curve), [ADR 0111](0111-a-mail-spool-retries-on-a-backoff-and-resends-only-after-a-crash.md) (the spool that parked its messages to get a growing delay), [ADR 0053](0053-sdk-events-domain.md) (the frontier), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (siblings, not methods), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (who owns a type), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (the refused settings), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (a field added to a published struct); kitsunium/platform#34 decision 4 and kitsunium/platform#35

## Context

The framework built on this SDK decided that a notification is never lost.
Its queues — a topic's publications, a queued command, the mail outbox, a
watch's notice — are today the SDK's file broker, and every one of them is
published from inside a unit of work that writes the application's database.
kit therefore HOLDS each publication until its transaction commits and
publishes it after, and its own documentation states what that costs: "a
process that dies between the commit and the release loses what was held".
There is no ordering of "commit, then publish" or "publish, then commit" that
closes the gap when the message and the write live in two stores: one of them
can always be done without the other.

The only fix is for the message to live in the SAME store as the write, in the
same transaction: the transactional outbox. The SDK already has the two
mechanisms that needs — `core/sql.Joiner`, which lets a callee handed only a
context run on the transaction it carries, and `core/sql.Deferrer`, which holds
an effect until that transaction commits (ADR 0139) — and a document store that
uses both. It had no queue that did.

The same issue asks three things of every broker:

- **A retry delay that grows.** `PolicyValue.RetryDelay` is a constant, so a
  downstream that is down is asked at the same cadence it is failing to keep
  up with. The mail spool worked around it by PARKING a failed mail — extending
  its own lease on a backoff and returning nil (ADR 0111) — and kit computed
  backoffs itself.
- **A failure no retry can fix, dead-lettered at once.** A payload that does not
  decode will not decode on the fifth attempt; retrying it spends
  `MaxDeliveries` leases and retry delays, and keeps out of the dead-letter
  store, for as long, the one record an operator needs to see.
- **A dead letter an operator can act on.** `DeadLetterReader` reads the store
  and, by decision, never changes it (ADR 0054 §D3). Nothing could put a
  message back once its downstream was fixed, or remove one that is obsolete.

`Broker` is frozen at four methods (ADR 0054 §D8, ADR 0039), and ADR 0053's
frontier table gives a queue "its own" transaction. Both constrain what follows.

## Decision

### D1 — a third broker, in the caller's database, and the frontier does not move

`NewSQL(SQLConfig) (Broker, error)` lives in `internal/service/queue` beside
`NewFile` and `NewMemory` — one package, one code range, one conformance suite,
as ADR 0139 §D2 argues for docstore's two engines. It keeps the queue in ONE
table of a PostgreSQL, MySQL or SQLite database the caller owns and hands over
as the `core/sql.Transactor` its own transactions are opened with, which must
also be a `Joiner` and a `Deferrer` (`SQL_QUEUE_MISCONFIGURED` otherwise). It
sends no statement at construction, imports no driver (ADR 0055 §D2), and
implements every sibling: `DeadLetterReader`, `LeaseExtender`, `Waker`, and
the two of D8 and D9.

**The frontier's Transaction row is about where the WORK runs, and it does not
move.** ADR 0053 §D1 wrote "its own" for the queue because a handler runs later,
on a consumer's goroutine, in a transaction of its own — which stays true. What
the SQL broker makes part of the publisher's transaction is the MESSAGE'S
EXISTENCE: published inside it, the message is receivable once it commits and
never existed if it rolls back. ADR 0053 §D1 sent a caller who wants both
behaviours to "publish the event AND enqueue the job"; with this broker the
enqueue is atomic with the write that caused it, which is exactly what that
sentence could not promise before. The core package doc and the facade say so
beside the table.

### D2 — every call runs where its context says

The rules are ADR 0139 §D5's, so a framework reasons about the queue and its
stores the same way:

- **Inside the caller's transaction**, every call that writes — a publication,
  a lease, an acknowledgement, a replay — is a SAVEPOINT of it: a statement
  that fails undoes itself alone and, on PostgreSQL, clears the aborted state
  it leaves, so the caller's transaction stays usable
  (`TestSQLAFailedPublicationLeavesTheCallersTransactionUsable`). A lease taken
  there stands or falls with the transaction.
- **Outside one**, a call of one statement runs on the pool, where one
  statement is atomic by itself; a Receive that leases runs in a transaction
  of the broker's own, READ COMMITTED on MySQL, as docstore's own are.
- **The wake waits for the commit.** A publication, a nack and a replay close
  the queue's wake signal through `Deferrer.Defer`: after the caller's commit,
  never for its rollback — a wake for a message a rollback took back would find
  nothing — and at once when the call committed on its own.

### D3 — one table, whose state is three columns

| column | holds |
|---|---|
| `id` | the message identifier — the enqueue instant zero-padded, a dash, eight random bytes in hex, the file broker's shape — binary, the key |
| `dead` | 0 for a live message, 1 for a dead letter |
| `due` | Unix nanoseconds: a queued message's visibility, a leased one's deadline, a dead letter's failure |
| `enqueued_at`, `deliveries` | the enqueue instant; the delivery count, the BROKER's (ADR 0054 §D3) |
| `lease` | NULL for a queued message; the lease's random half for a leased one |
| `payload` | the bytes published — binary, and NULL-able, which a nil payload binds as |
| `reason`, `cause`, `code` | a dead letter's cause, bounded as the file broker bounds its record |

Every live row whose `due` has passed is either receivable or a lease whose
holder may have died, so ONE ordered read finds both, over the index a
`UNIQUE (dead, due, id)` constraint builds. That constraint can never be
broken — `id` is the key — and is how PostgreSQL and SQLite, which declare no
plain index inside `CREATE TABLE`, get the index without a second statement:
docstore's device (ADR 0139 §D3).

- **The instants are the broker's clock, never the database's `NOW()`.** A test
  drives every deadline with a `ManualClock`, as for the other two brokers,
  and processes over one table compare instants their own clocks wrote — the
  file broker's premise, stated rather than changed. A reading the table
  cannot hold — before 1970, or so late that a deadline computed from it passes
  2262 — is refused (`QUEUE_MISCONFIGURED`, field `Clock`) before any statement,
  where it would have minted an identifier its own receipts are refused under,
  or wrapped a lease's deadline negative.
- **A table name** is a lower-case identifier of at most `MaxSQLTableLen` (63,
  PostgreSQL's limit) bytes that holds no `___` — docstore derives its tables
  with it, so a queue can never be one of them — and does not start with
  `sqlite_`. It is interpolated, quoted, and that validation is the whole
  defence.
- **`SQLMigration(dialect, table, version)`** creates the table in one
  `CREATE TABLE IF NOT EXISTS`, named `queue <table>`; its Down drops it. The
  SDK numbers nothing (ADR 0055 §D12).

### D4 — an idle Receive is one read; a Receive that leases is one transaction

- **The probe.** Outside a transaction, `Receive` first reads
  `SELECT MIN(due) FROM <table> WHERE dead = 0`. Nothing live, or nothing due
  yet: it records that instant for `Wake` and returns — one indexed read, no
  transaction, and none of SQLite's one write lock, which every writer of the
  application shares. Consumers asleep on the `Waker` (ADR 0104) cost their
  database one such read per poll.
- **The lease.** Something is due: a transaction takes, on SQLite, the write
  lock first with a write that writes nothing (ADR 0140's statement — a
  transaction that read first could be refused busy at its update, its
  snapshot stale), reads the due rows in `(due, id)` order — `FOR UPDATE SKIP
  LOCKED` on PostgreSQL and MySQL, so two consumers never wait on each other's
  messages and never take the same one — and leases them in ONE statement:
  one deadline, one random half, the count moved on, 500 identifiers a
  statement under SQLite's bound-parameter ceiling. A lapsed lease with an
  attempt left is leased again; one without is dead-lettered with
  `LEASE_EXPIRED` by the same read, as the other brokers' reclaim does. There is
  no sweeper, here either.
- **The next instant.** A Receive that did not fill its batch reads
  `MIN(due)` after now and records for `Wake` the earlier of that and the
  deadline a lease taken now gets — its own, or one another consumer was
  taking on a row the locked read skipped, which the read cannot see; early
  costs one empty Receive, late would cost a poll. One that filled its batch
  records now. The probe keeps NULL — nothing live — apart from an instant of
  zero. Inside the caller's transaction the probe is skipped: on MySQL a plain
  read answers from the transaction's snapshot, where the locking read does
  not.

### D5 — ending or renewing a lease is one statement

`Ack`, `Nack`, `Extend` and `Reject` each send one statement whose condition is
the receipt's identifier, lease and delivery count, a live row, and a deadline
still ahead of the instant the call read. Zero rows is `LEASE_EXPIRED`: the
lease lapsed — whether or not anybody reclaimed it, as ADR 0054 §D2 requires —
or was renewed, reclaimed or ended by another holder. Every UPDATE changes a
column, which keeps MySQL's affected-row count exact whatever
`CLIENT_FOUND_ROWS` says.

A receipt is `<id>.<count>.<lease>` and is refused `UNKNOWN_RECEIPT` by SHAPE,
as the file broker's is: two brokers over one table, in one process or in
twenty, are one queue, so a receipt minted elsewhere is legitimate
(`TestSQLTwoBrokersOverOneTableAreOneQueue`). A count edited in a receipt
matches no row.

### D6 — what fails, and what is said about it

- A statement the database did not complete is `QUEUE_BACKEND_FAILED`
  (`0.3.53.1`, the file broker's code, its Private widened to say "a filesystem
  call or a SQL statement"), JOINED with the driver's error — never wrapped, so
  the verdict stays the origin — whose text is WITHHELD from every rendering:
  a driver quotes the row a statement touched, and a queue's row is a payload.
  docstore's `withheld`, kept per package because each package's errors are its
  own. The transactor's own verdicts pass through.
- A configuration no SQL broker could run is `SQL_QUEUE_MISCONFIGURED`
  (`0.3.53.5`), naming the setting and the problem; a policy is refused by the
  guard every broker shares.

### D7 — a retry delay that grows: `PolicyValue.MaxRetryDelay`

`PolicyValue` gains `MaxRetryDelay`. Positive, a message nacked on its n-th
delivery waits `RetryDelay × 2^(n−1)`, held at the ceiling —
`resilience.Backoff`'s curve (ADR 0103), which the brokers call through one
function, `retryDelay`, so the double waits exactly what the durable brokers
wait. Zero keeps `RetryDelay` constant: the behaviour every policy had, the
zero's one reading, and the reason the field is backward compatible
(ADR 0040's licence to add a field, and nothing else).

A ceiling that is no ceiling is refused, by field and problem
(`QUEUE_MISCONFIGURED`): negative; past `MaxDeadlineOffset`, whose deadline
could not be recorded; above no `RetryDelay`, since growth from zero is zero
forever — an inert knob (ADR 0031); below `RetryDelay`, which would silently
shorten every wait asked for. The curve is keyed on the delivery count because
that is what the broker keeps — a lapsed lease counts as a failed attempt, as
it does for `MaxDeliveries` — and its jitter stays at zero: a retry's instant is
what `Wake` reports and what a test asserts to the nanosecond.

### D8 — a failure no retry can fix: `DoNotRetry`, `NotRetryable` and `Rejecter`

- **The handler's half is a mark, not a type.** `DoNotRetry(cause)` is an
  `errs.Wrap` onto the cause with `CodeNotRetryable` (`0.2.23.6`) as the
  wrap-site code. Origin wins (CLAUDE.md rule 6): an SDK cause keeps its own
  Reason, Code and Public — the three things a dead letter records, which ADR
  0054 §D10 refused to let an engine verdict displace — and the mark rides the
  wrap trail, where `errs.HasCode` finds it. A foreign cause, or none, records
  `NOT_RETRYABLE` itself, whose Public says what happened. `errors.Is(err,
  NotRetryable)` sees only an origin, which is why the code is published and the
  documentation says to match it.
- **The broker's half is a sibling.** `Rejecter.Reject(ctx, receipt, cause)`
  moves a leased message to the dead-letter store at once, recorded as a last
  nack records it, at the count it had — so `DeadLetterValue.Deliveries` may now
  be below `MaxDeliveries`, and its documentation says when. It refuses a lapsed
  or unknown receipt as `Ack` does. It is a sibling because `Broker` is frozen
  and because a connector with no in-band dead-letter move cannot implement it.
- **`Consume` joins them.** A handler failure carrying `CodeNotRetryable`,
  against a broker that is a `Rejecter`, is rejected; against one that is not,
  it is nacked as before — the shortcut is lost, never the message. A panic is
  never the mark: `HANDLER_PANICKED` carries the recovered value as a field,
  never as its origin.

### D9 — a dead letter is a decision: `DeadLetterManager`

`ReplayDeadLetter(ctx, id)` and `DeleteDeadLetter(ctx, id)`, by
`MessageValue.ID`, on one sibling — the two decisions an operator takes after
reading the store. A replay queues the message again, visible at once, with
its identifier, payload and enqueue instant kept — the log lines of both its
lives join on one ID — and its count reset, so it has every attempt again; the
record, its cause included, is gone once it is queued. An identifier the store
does not hold is `DEAD_LETTER_NOT_FOUND` (`0.2.23.7`, HTTP 404, `EX_NOINPUT`).

Each broker keeps the promise its storage allows, and says which:

- **memory** moves the record back into its ready list;
- **file** publishes the payload into `ready/` atomically and durably, as
  `Publish` does, and removes every record of the identifier AFTER — the order
  that degrades into a duplicate rather than a loss. A record whose header
  names another message than its file name does — truncated, or planted — is
  never queued. Two replays racing each
  other can therefore queue it twice, which at-least-once permits: a record
  cannot be renamed into the queue, because a queued message is its payload
  alone, so there is no rename for the loser to lose;
- **SQL** changes the row's state in one UPDATE, so the second of two racing
  replays is refused `DEAD_LETTER_NOT_FOUND`.

### D10 — every new name is public

`pkg/v1/queue` publishes `SQLConfig`, `NewSQL`, `SQLMigration`,
`MaxSQLTableLen`, `DoNotRetry`, `Rejecter`, `DeadLetterManager`,
`NotRetryable`, `DeadLetterNotFound`, `SQLQueueMisconfigured`, and the codes
`CodeNotRetryable`, `CodeDeadLetterNotFound` and `CodeSQLQueueMisconfigured`.
`Rejecter`, `DeadLetterManager`, `DoNotRetry` and the two core sentinels are
owned by `core/queue`, because the port speaks them; `SQLConfig` and its
sentinel are the SQL engine's (ADR 0074). A test in the facade builds every
one of them without an internal import, so no public signature returns a type
a downstream module cannot name.

## Consequences / Semantics

- **How kit adopts it.** A queue on a database is
  `queue.NewSQL(queue.SQLConfig{Transactor: <the database's transactor>,
  Dialect: <its dialect>, Table: <a kit table>, Policy: <the queue's policy>})`,
  its table created by `queue.SQLMigration(dialect, table, version)` in kit's
  own set, registered like a store's table. A publication made with the
  context kit hands a store call on that database — the one carrying its
  `kit.Transact` scope — joins the scope's transaction as a savepoint, so the
  hold-and-release kit does for a publication on a database has nothing left to
  hold: the message is the outbox row, and a crash after the commit loses
  nothing. A publication outside a transaction is one INSERT, durable when it
  returns. Its consumers run under a supervisor (item 5 below).
- **What each call sends**, pinned by `TestSQLStatementsPerCall`:

| call | statements |
|---|---|
| `Receive` on an idle queue | 1 read |
| `Receive` that leases a full batch | the read, BEGIN, (SQLite's lock), the locked read, the lease, COMMIT |
| `Receive` that leases a partial batch | the same, and the next-due read |
| `Publish`, `Ack`, `Nack`, `Extend`, `Reject`, a replay, a deletion | 1 |
| any write inside the caller's transaction | its statement between a SAVEPOINT and a RELEASE |

- **A growing delay, immediate dead-lettering and the two dead-letter decisions
  hold on all three brokers**, pinned by the same table-driven cases; the mail
  spool may keep parking, and a new consumer need not.
- **The default suite** runs the conformance cases over every broker — the SQL
  one on each dialect's statements over a fake engine written for it, as
  docstore's is — under the race detector. The integration lane runs the SQL
  broker on SQLite, PostgreSQL 17 and MySQL 8.4 through real drivers, sixteen
  concurrent consumers included.

## What this broker does NOT guarantee

1. **A wake across processes.** A publication in another process is found by
   the poll, as for the file broker; PostgreSQL's `LISTEN/NOTIFY` is a driver
   feature `database/sql` does not carry.
2. **Anything past the caller's isolation.** A call inside the caller's
   transaction runs at its isolation: on PostgreSQL REPEATABLE READ, a row
   another transaction changed since the snapshot is a serialization failure,
   which the caller gets as `QUEUE_BACKEND_FAILED`.
3. **Old servers.** SKIP LOCKED needs MySQL 8.0.1 or MariaDB 10.6, and SQLite
   3.35 is already docstore's floor; an older server fails the lease with
   `QUEUE_BACKEND_FAILED`.
4. **Clocks that disagree.** Deadlines are the brokers' clocks; processes whose
   clocks disagree by more than a lease's margin redeliver early or late.
5. **A consumer that outlives its database's bad moment.** `Consume` returns
   the broker's error when the storage fails (ADR 0054 §D10), and a database
   fails transiently — a failover, a dropped connection, a deadlock, SQLite's
   busy answer — where a directory rarely does. A consumer over the SQL broker
   is therefore run under `lifecycle`'s supervisor (ADR 0112), which restarts it
   on the published backoff; `Consume` itself does not retry a storage failure.
   SQLite is opened with a busy timeout, without which a held write lock is a
   failure at once, as ADR 0139 §D9 says for docstore.
6. Everything ADR 0054 already declines — exactly-once, order under retry.

## Breaking changes

None. The broker, the siblings, the mark, the sentinels, their codes and the
facade names are additions; `PolicyValue.MaxRetryDelay`'s zero is the old
behaviour; `Broker` and `Handler` are unchanged. Two things read differently:
`QUEUE_BACKEND_FAILED`'s Private now names SQL statements too (log-only), and a
`DeadLetterValue.Deliveries` below `MaxDeliveries` now means a rejected message.

## Alternatives considered

- **An outbox table relayed into the file broker.** Two stores, a relay that
  must itself be supervised, elected and made idempotent, and a second
  at-least-once hop for every message. The queue IS the outbox.
- **The queue over docstore's SQL store.** A document store has no lease, no
  claim that skips another consumer's rows, and no ordered index on an instant
  (ADR 0139 §D10 left one to a later sibling). A queue's defining read is an
  ordered claim.
- **Three tables, mirroring the file broker's three directories.** Every
  transition would move a row — payload and all — between tables, where one
  UPDATE of three columns suffices, and the lapsed-lease scan would read a
  second index.
- **Plain `FOR UPDATE`, or an advisory lock.** Consumers would queue behind each
  other's locked rows, or behind one lock, and the throughput of N consumers
  would be one consumer's.
- **The database's `NOW()`.** It would take the clock out of the tests and put
  a second clock beside the one every other instant of the domain is read on;
  the brokers stay on the caller's.
- **A custom error type for "do not retry".** It would sit outside the SDK's
  error model (CLAUDE.md rule 2), and matching it would need `errors.As` on a
  concrete type; the wrap trail is where the model already puts "this error
  passed through here".
- **`NotRetryable` as the origin, so `errors.Is` answers.** The dead letter
  would then record NOT_RETRYABLE instead of the handler's reason, which is the
  displacement ADR 0054 §D10 refused.
- **A fifth `Broker` method, or a `dead bool` on `Nack`.** `Broker` is frozen;
  a sibling is the only extension.
- **A multiplier and a jitter on the policy.** One curve serves every caller who
  asked; a jitter would move the instant `Wake` reports. Deferred, not refused.
- **A replay under a new identifier.** It would cut the join between the log
  lines of the message's two lives.

## Deferred

- A wake across processes (`LISTEN/NOTIFY` on PostgreSQL; MySQL has no
  counterpart).
- Bulk decisions — replay every dead letter, purge those older than an instant —
  and a retention for the dead-letter store.
- Timing the SQL broker on the real engines beside docstore's
  (`third-party/db/sql/BENCH.md`).
- A fallback for servers without SKIP LOCKED.
- A multiplier and a jitter for the growing delay.
- ADR 0054's own deferrals — per-key ordering, priorities, `PublishAt`, batch
  acknowledgement — unchanged.

## Verification

- `internal/core/queue`: `TestAGrowingRetryDelayNeedsARealCeiling`,
  `TestDoNotRetryMarksTheFailureAndKeepsTheCauseItsOrigin`,
  `TestTheQueueSiblingsKeepTheirMethodCounts`, and the seven codes and
  sentinels in `queue_external_test.go`.
- `internal/service/queue`, over every broker — memory, file, and SQL on each
  dialect's statements over `sqlfake_external_test.go`: the whole conformance,
  order and wake suites, plus `TestAGrowingRetryDelayFollowsTheBackoffCurve`,
  `TestARejectedMessageIsDeadLetteredAtOnceWithItsCause`,
  `TestRejectRefusesWhatAckRefuses`,
  `TestAReplayedDeadLetterComesBackAsItsFirstDelivery`,
  `TestADeletedDeadLetterIsGoneAndASecondDecisionIsRefused`,
  `TestAReplayWakesAnIdleConsumer`,
  `TestAHandlerThatSaysDoNotRetryIsDeadLetteredOnItsFirstFailure` and
  `TestABrokerThatCannotRejectRetriesADoNotRetryFailure`; for the SQL broker,
  `TestSQLAPublicationExistsIfAndOnlyIfItsTransactionCommits`,
  `TestSQLAFailedPublicationLeavesTheCallersTransactionUsable`,
  `TestSQLAStorageFailureWithholdsTheDriversText`, `TestSQLStatementsPerCall`,
  `TestSQLALargeBatchIsLeasedInBoundedStatements`,
  `TestSQLAReceiveInsideTheCallersTransactionStandsOrFallsWithIt`,
  `TestSQLTwoBrokersOverOneTableAreOneQueue`, the receipts, the refusals, the
  statements as text and the DDL per dialect.
- `pkg/v1/queue/sql_external_test.go`: every new name through the facade.
- `third-party/db/sql/queue_integration_test.go`, under `-tags integration`, on
  SQLite in process, PostgreSQL 17 and MySQL 8.4 — every statement the broker
  sends: the lifecycle, an extended lease and an empty payload, lapsed leases
  buried, a partial batch, the stream's order and a retry stepping aside, the
  caller's transaction and the wake its commit sends, sixteen consumers never
  sharing a message, `Consume` rejecting, a real driver's text withheld. The
  full conformance table cannot run there: its cases are `internal/service/queue`
  tests, and that module imports no driver (ADR 0055 §D2) — the fake engine
  runs them on each dialect's statements instead, as docstore's does.

## References

- `internal/service/queue/sql*.go`, `retry.go`, `file_replay.go`, `consume.go`
- `internal/core/queue/deadletter.go`, `retry.go`, `policy.go`
- `internal/service/queue/CLAUDE.md` §"The SQL broker"
- PostgreSQL: `SELECT … FOR UPDATE SKIP LOCKED`, `bytea`, savepoints and the
  aborted transaction; MySQL 8.4: `SKIP LOCKED` (since 8.0.1), affected rows,
  READ COMMITTED; SQLite: the write transaction a write statement starts
- kitsunium/platform `kit/transact.go` — the held effects this replaces for a
  publication on a database
