# internal/service/data/stream/

The reference half of this file moved, unchanged and under the same section titles, to
`internal/service/data/stream/NOTES.md` (ADR 0253): read the section a change touches.
It holds:
§Contents; §The conformance suite, and adding an engine.

## Purpose

The engines of `internal/core/data/stream`'s `Stream` port (ADR 0231) and the
conformance suite every one of them runs. Today: the memory engine,
`NewMemory` (#819), and the file engine, `NewFile`, a directory of segments
over `kernel/fs/seglog` with retention by age and bytes, `Trim` and
`OffsetAt` (#821, ADR 0231's amendment), and the SQL engine, `NewSQL`, three
tables of the caller's database joined to the transaction its context
carries on ADR 0151's pattern (#822, ADR 0231's second amendment), and the
subscriber, `Subscribe`, that reads a stream for a consumer group — one
worker per partition, the group's offset committed after the handler or by
the handler in its own transaction (#823, ADR 0231's third amendment). Every
engine also compacts by key (#824, ADR 0231's fourth amendment).

A sibling writes to it: `docstore`'s change feed (#831) appends one record
per document write to any `Stream` through the `core/data/stream` port — the
SQL engine here, on the store's own transactor, joins the write's
transaction — and its tests run it on `NewMemory` and `Subscribe`.

## Surface

| Symbol | What it is |
|---|---|
| `NewMemory(MemoryConfig) (corestream.Stream, error)` | the in-process engine, the test double of every durable one; also a `Waker`, `Seeker`, `Trimmer`, `Compactor` (in place) and `Committer` |
| `NewFile(FileConfig) (corestream.Stream, error)` | the durable engine: `<Dir>/p<partition>/` one segmented log each, `<Dir>/cursors/` one file per group and partition; also a `Waker`, `Seeker`, `Trimmer`, `Compactor` (segments rewritten through `seglog.Rewrite`), `Committer` and `io.Closer`, the directory locked until `Close` |
| `NewSQL(SQLConfig) (corestream.Stream, error)` | the engine over `<Table>`, `<Table>__heads` and `<Table>__cursors`: an `Append` or a `Commit` inside the caller's transaction is a savepoint of it, the `Waker` fires through `Deferrer` after its commit; also a `Waker`, `Seeker`, `Trimmer`, `Compactor` (bounded DELETEs by offset) and `Committer`; a `RetainBytes` or `MaxBytes` is refused (`SQL_STREAM_MISCONFIGURED`) |
| `SQLConfig{Transactor, Dialect, Table, Policy, Clock}` | the transactor (a `Joiner` and a `Deferrer`), the dialect, the table (lower-case words joined by single underscores, at most 54 bytes, not `sqlite_…`), the policy and the engine's clock (`clock.Clock`, nil: `clock.System`) |
| `SQLMigration(dialect, table, version) (coresql.MigrationValue, error)` | the three `CREATE TABLE IF NOT EXISTS`, checksummed; Down drops them |
| `FileConfig{Clock, Dir, Policy}` | the clock that stamps and ages, the stream directory (the queue's directory rules), and the policy, refused at construction by the core's `Validate` and by a `MaxRecordBytes` one segment record cannot hold |
| `Subscribe(ctx, Stream, SubscriberConfig) error` | one worker per assigned partition, offset order, the group's offset moved past each accepted batch; a refused batch retried on `Retry` and blocking its partition alone, narrowed then parked with `Park`; nil at the stop, `SUBSCRIBER_MISCONFIGURED`, or the medium's error |
| `SubscriberConfig{BatchSize, Clock, Commit, Fence, Group, Handler, HandlerIsIdempotent, Name, Park, Partitions, PollInterval, Retry, SettleTimeout}` | `Group`, `Handler` and `HandlerIsIdempotent` required; `Partitions` empty is every one; `Fence` a `core/app/lock.Locker` (`Name` then required); zero `PollInterval` 100 ms, `SettleTimeout` 5 s, `Retry` 100 ms doubling to 30 s |
| `CommitMode`: `CommitAfterHandler` (zero), `CommitByHandler` | who commits an accepted batch: the subscriber after the handler (at least once), or the handler inside its transaction (exactly once in effect on the SQL engine) |
| `Handler` | `func(ctx, []corestream.EntryValue) error`: one partition's batch, never empty |
| `ParkConfig{After, Broker}` | the dead letters' queue and how many consecutive refusals park a record; a dead letter's payload is the entry's JSON |
| `MemoryConfig{Clock, Policy}` | the `clock.Timed` that stamps, ages and times the backpressure wait (nil: `clock.System`), and the `corestream.PolicyValue`, refused at construction by the core's `Validate` |

## Where the file engine runs

The groups' cursors are published through `service/data/vfs`'s disk
filesystem, so `NewFile` runs where `vfs.NewOS` does. On Windows, illumos and
Solaris vfs refuses by design (ADR 0018), and `NewFile` refuses with the same
`UNSUPPORTED_PLATFORM`, under `STREAM_BACKEND_FAILED`. Every file-engine case
starts with `requireFileEngine` (`file_external_test.go`): where vfs refuses,
it asserts that `NewFile` refuses the same way, then skips with the reason
(ADR 0095); the predicate is vfs's refusal, never a list of GOOS.

## The decisions

- **The memory engine is the test double, not a lightweight stream.** It is
  one process and holds nothing on a device; it exists so that the durable
  engines can be held to the same cases and a consumer can test without a
  directory (the queue's reasoning, ADR 0054).
- **Retention by age is lazy**, as the file engine's must be: a read skips
  the expired prefix under the read lock; an `Append`, a `Trim` or the
  backpressure wait removes it. Nothing runs a timer.
- **`AppendedAt` never goes backwards**: a clock set back stamps the batch
  with the previous instant, so a partition stays sorted by time and
  `OffsetAt` and the age retention are binary searches.
- **`MaxBytes` bounds the whole stream, `RetainBytes` one partition.** A
  `RetainBytes` trim never removes the batch its own `Append` just wrote; a
  batch above `MaxBytes` is refused at once, since no removal can make room
  for it.
- **The file engine acts on the first offset at once and on the device by
  whole segments.** A bound or a `Trim` makes records unreadable at that
  instant, exactly as the memory engine does — the suite holds both to the
  same cases —, and the `Append` or `Trim` that moves the first offset
  writes a seglog drop record in its own batch, after whose sync every
  sealed segment holding nothing readable is unlinked. No timer: the age
  bound is computed at read time and applied by the next `Append`, `Trim`
  or backpressure wait.
- **One batch, one sync, one writer.** An `Append` is one seglog batch, two
  fdatasyncs whatever its size; the directory's `lock` file is held with
  `kernel/fs/flock` for the engine's life, so a second engine over one
  directory is refused, never a second writer — and the open engine's
  Waker is the directory's.
- **The file engine's index is memory, rebuilt at open** by reading the log
  once: every record carries its batch's instant, so `OffsetAt`, the age
  bound and `Bounds` never read the device, and `Read` is one `pread` per
  contiguous run, whatever the backlog (`BENCH.md`).
- **Bytes are copied on the way in and on the way out**, each in one arena:
  two allocations per Append and per Read whatever the batch (`BENCH.md`)
  on the memory engine; the file engine's `Read` hands out the bytes its one
  read returned.

- **The SQL engine serialises the appenders of a partition on its head
  row.** On PostgreSQL and SQLite an Append's first statement is an upsert
  that creates the head row or writes it back unchanged — the row lock on
  PostgreSQL, the write that takes SQLite's lock first (ADR 0140) — held
  until the transaction ends, so offsets are assigned in commit order and a
  reader following a partition never passes a record that commits later.
  Then the head is read (`FOR UPDATE`), the records inserted 199 per
  statement, the head advanced: 3 + chunks statements. A rolled-back Append
  takes its offsets back with it.
- **On MySQL the head is created apart, then locked by reading it — never
  by the upsert** (#822). Sixteen transactions upserting a missing head
  deadlocked once the one that inserted it rolled back, and InnoDB rolled a
  caller's transaction back whole. The row is created by `INSERT IGNORE`,
  alone and committed on the pool — before the engine's own transaction
  outside a caller's, after an unlocked read found none inside one (a second
  connection, once per partition) —, then locked by the head's read
  `FOR UPDATE`; once known it is locked at once: 2 + chunks statements.
  Never send the upsert on MySQL. Why, and the fake that proves it:
  `NOTES.md` §The head on MySQL.
- **The ordering test must be able to fail.** The serialising fake cannot
  tell whether the head is locked: a change to the head's statements is
  judged by `TestSQLOffsetsCommitInOrderUnderConcurrentAppenders` and
  `TestSQLAFreshPartitionOutlivesItsFirstAppenderRollingBack` on the
  concurrent fake (`sqllocks_external_test.go`), and the negative control
  `TestSQLTheOrderingTestFailsOnAnEngineWithoutRowLocks` must keep failing
  the race on a fake that locks nothing.
- **The SQL engine's Read is one unlocked statement**: the head row LEFT
  JOINed with the readable records, and the head's floor beside the highest
  stored offset past the age bound: the first readable offset is the floor
  or the one after that, whichever is higher (#824). Instants follow
  offsets, so a compaction's holes never move it, and the age sweep raises
  the floor before it deletes the expired rows.
- **The SQL engine's Trim moves a floor, then deletes.** The head's
  `first_offset` is raised in one statement under the head's lock, so the
  records below are unreadable at once; the rows go in DELETEs of at most
  1 024 offsets. Age retention is read-time (`appended_at > now - RetainFor`)
  and its rows are deleted by an Append at most once per `min(RetainFor,
  1 min)` per partition and engine — a failed sweep is retried by the next.
- **The SQL engine keeps no byte count**: `RetainBytes` and `MaxBytes` are
  refused at construction rather than ignored.
- **Compaction is one decision on every engine** (`compact.go`): among the
  records at least `CompactAfter` old, a key's last stays and every earlier
  one goes, a tombstone goes once twice `CompactAfter` old, an unkeyed
  record stays; a younger record is never touched. Offsets keep, holes are
  skipped by `Read`, `Bounds` does not move. Nothing runs it on its own.
- **The file engine's hole is a record**: a dropped record is rewritten
  through `seglog.Rewrite` as its batch's instant alone (8 bytes, a length
  no encoded record has), same offset, same kind byte, so the index, the
  batches and the age bound are untouched and a reopen reads the hole; its
  key and payload bytes leave the prefix sums and `MaxBytes`' count.
- **The SQL engine compacts in the engine, not in SQL**: the rows are read a
  page of 1 024 at a time without their payload, the decision made by
  `compactLines`, the dropped rows deleted by offset, 256 a statement and
  only while still readable — no dialect-specific `NOT IN (SELECT …)`.
- **A group's cursor is keyed by the SHA-256 of its name**, so no group is
  too long for a key; the name is kept beside it for an operator.

- **The subscriber is `queue.Consume` for a stream, with one worker per
  partition rather than `Parallelism`**: a partition is read by one worker,
  in order, and a refused batch blocks it — the per-key ordering ADR 0054
  deferred, its head-of-line blocking the design rather than a defect. The
  other partitions carry on. Assignment is static (`Partitions`); a dynamic
  rebalance needs membership, which is consensus's problem.
- **A group's first position is the first retained offset**, never the
  end: a record appended before the group's first subscriber is delivered.
  An offset retention removed resumes at the first one kept.
- **The subscriber commits past a parked record in both modes**: the
  handler never accepted it, so nothing else would move the group.
- **The fence narrows the window; it does not close it.** A worker renews
  its lease before every turn and before every commit it makes, and a
  refused renewal gives the partition up uncommitted; a handler that runs
  past its lease can still commit through `CommitByHandler`. The stores do
  not compare the lease's fencing token.

## Do NOT

- Declare an error code here (ADR 0160): `STREAM_BACKEND_FAILED`,
  `STREAM_DIRECTORY_UNUSABLE`, `SQL_STREAM_MISCONFIGURED` and
  `SUBSCRIBER_MISCONFIGURED` are `core/data/stream`'s; a new one goes in the
  design's `0.3.94.*` block.
- Send a statement from the SQL engine that `sql_dialect.go` did not render,
  or let a driver's error reach a rendering: `failed` joins it under
  `svcsql.Withheld`.
- Place a key on a partition any other way than `core/data/stream.PartitionOf`.
- Write a conformance case for one engine only: it goes over `everyStream`, or
  in that engine's own `*_external_test.go` when it is truly its alone.
- Import `internal/service/app/consensus`: data sits below app (ADR 0155); the
  segmented log is the kernel's (`kernel/fs/seglog`, ADR 0230).
