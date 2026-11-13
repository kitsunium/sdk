# ADR 0231 — a record is kept until its retention ends, and every group reads it at its own pace

- **Status**: Accepted
- **Date**: 2026-11-12
- **Deciders**: SDK maintainers
- **Amended**: 2026-11-12 for the file engine (#821), the SQL engine (#822), the subscriber (#823) and compaction (#824), below.
- **Amends**: nothing. A new domain, `data/stream` (`internal/core/data/stream`, `internal/service/data/stream`, later `pkg/v1/data/stream`), and two code ranges, `0.2.38.*` (the core's) and `0.3.94.*` (the engines', declared in the core — ADR 0160).
- **Related**: [ADR 0054](0054-sdk-queue-domain.md) (the queue, the right-hand column of the frontier below), [ADR 0053](0053-sdk-events-domain.md) (the in-process bus), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a frozen port and its siblings), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (the policy's zeros), [ADR 0151](0151-a-message-published-in-a-transaction-exists-if-and-only-if-it-commits.md) (joining the caller's transaction), [ADR 0155](0155-every-layer-groups-its-packages-by-family-and-a-path-may-move-while-v0.md) (the `data` family), [ADR 0160](0160-every-service-has-a-core-and-a-code-keeps-its-value-when-it-moves.md) (where a code is declared), [ADR 0163](0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md) and [ADR 0164](0164-an-error-code-is-the-designs-and-kit-writes-it.md) (design first), [ADR 0230](0230-a-segmented-log-is-a-kernel-primitive-that-writes-and-never-decides.md) (the segmented log the file engine builds on), issues [#818](https://github.com/kitsunium/sdk/issues/818) (this decision) and [#890](https://github.com/kitsunium/sdk/issues/890) (the program)

## Context

Every durable messaging primitive the SDK has is a queue. A message exists
until one consumer acknowledges it — `Ack` "is the only call that does"
remove it (`internal/core/data/queue/gen_ports.go`) —, a second independent
reader is impossible, the order is lost at the first retry and at
`Parallelism` 2 ("Above 1 there is NO ordering left, in any sense",
`internal/service/data/queue/consume_config.go`), and nothing can be read
again once acknowledged. ADR 0054 deferred per-key ordering by name, because
its honest version blocks a key behind a failing message.

The owner asked for an internal Kafka: a retained, offset-addressed,
partitioned sequence read by independent consumer groups. None of those four
properties can be added to `Broker` without breaking ADR 0054's contract — the
frozen port, at-least-once by lease, removal at acknowledgement. A new domain
is the only honest placement, and ADR 0163 requires its design before any
code.

## Decision

1. **A new domain `data/stream`**, in the `data` family (ADR 0155: the
   messages one process leaves for another are data's). Not `log` (it
   collides with `observe/logger` and consensus's `LogStore`), not `topic`
   (the framework's word for its queue-backed pub/sub). Its design is
   `design/data/stream.yaml`; this change declares the core and the engines'
   package, and no engine.

2. **The frontier.** The queue keeps its column; the stream takes the other:

   | | `data/queue` (ADR 0054) | `data/stream` (this ADR) |
   |---|---|---|
   | A record ends | at its acknowledgement | when its retention ends — by age, bytes, `Trim` or compaction |
   | A reader holds | a lease on one message | an offset of its own in each partition |
   | Readers | competing consumers of one queue | any number of independent groups, each at its own pace |
   | Order | none past the first retry | per partition, always; a key's records in one partition |
   | A failure | retried, then dead-lettered | the reader parks at its offset; the record stays, every other group goes on |
   | Replay | impossible after `Ack` | any retained offset, or by time (`Seeker`) |

3. **The port `Stream` is FROZEN at four methods** (ADR 0039):
   `Append(ctx, []RecordValue) (AppendValue, error)` — one batch to one
   partition under one sync —, `Read(ctx, partition, from Offset, max)
   ([]EntryValue, error)` — non-blocking, empty at the end —,
   `Bounds(ctx, partition) (BoundsValue, error)` and `Partitions() int`. The
   siblings, reached by type assertion: `Waker` (a channel closed by the next
   append in this process — a hint, as the queue's), `Seeker.OffsetAt` (by
   time), `Trimmer.Trim` (a prefix dropped before its retention),
   `Compactor.Compact` (the last record of each key kept, offsets never
   renumbered) and `Committer.Commit` / `Committed` (a group's durable cursor:
   the offset it reads next).

4. **The reader owns its offset, the engine only keeps it.** A record is not
   consumed by being read, so nothing about a read lives in the record. A
   group that never committed is `UNKNOWN_GROUP`, never offset zero: "from
   the start" and "from the end" are both legitimate first positions and only
   the reader knows which it wants. A read below the first retained offset is
   `OFFSET_TRIMMED`, never a silent skip, so a reader learns it lost records.

5. **A key's partition is `PartitionOf`, in the core**: the 32-bit FNV-1a
   hash of the key modulo the partition count, so the memory, file and SQL
   engines place every key alike. A batch belongs to one partition, the one
   its keyed records hash to; a batch whose keys name two partitions is
   refused `PARTITIONS_MIXED` (`0.2.38.9`, beyond the issue's list) rather
   than split, because a split batch is two syncs and a crash between them
   keeps half of it. A batch with no key goes to the partitions in turn. The
   partition count is fixed for the life of a stream: changing it would move
   every key.

6. **`PolicyValue` works at its zero** (ADR 0031's clamp branch, every zero
   having one reading): `Partitions` 1, `MaxRecordBytes`
   `DefaultMaxRecordBytes` (1 MiB, the queue's bound and Kafka's), `RetainFor`
   and `RetainBytes` unbounded, `MaxBytes` unbounded, `BlockTimeout` refusing
   at once, `SegmentBytes` `DefaultSegmentBytes` (64 MiB, seglog's). Refused
   by field: a negative value, a `BlockTimeout` with no `MaxBytes`, a
   `MaxBytes` below one record. Above `MaxBytes`, `Append` waits up to
   `BlockTimeout` for retention to make room, then refuses `STREAM_FULL` —
   the producers' backpressure the queue does not have.

7. **Codes.** `0.2.38.*` in the core: `STREAM_MISCONFIGURED`,
   `RECORD_TOO_LARGE`, `OFFSET_TRIMMED`, `OFFSET_AHEAD`, `UNKNOWN_PARTITION`,
   `INVALID_BATCH_SIZE`, `STREAM_FULL`, `UNKNOWN_GROUP`, `PARTITIONS_MIXED`.
   `0.3.94.*`, the engines' and the subscriber's, declared in the core at the
   engines' path (ADR 0160): `STREAM_BACKEND_FAILED`,
   `STREAM_DIRECTORY_UNUSABLE`, `SQL_STREAM_MISCONFIGURED`,
   `SUBSCRIBER_MISCONFIGURED`. Both ranges are allocated in
   `design/sdk.yaml`'s `codes.ranges` in this change.

## Deferred, by name

- The engines: memory and the conformance suite (#819), the file engine over
  `kernel/fs/seglog` with retention and `OffsetAt` (#821), the SQL engine
  joined to the caller's transaction on ADR 0151's pattern (#822).
- Consumer groups and the subscriber, one worker per partition, a group's
  offset committed in the handler's own transaction (exactly-once in effect,
  #823).
- Compaction's record shape — a keyed record with an empty payload as a
  tombstone — and how long a tombstone survives (#824).
- The `pkg/v1/data/stream` facade (#825) and the framework's streamed topics
  (#826).

## Consequences

- The queue is untouched: `forward`, jobs and the framework's topics keep it
  where removal at acknowledgement is what they need.
- `internal/service/data/stream` exists before its first engine, holding only
  its package comment, because ADR 0160 puts the `0.3.94.*` codes in the core
  at an engine's path and the core-symmetry guard requires that path to be a
  package. Its first engine (#819) fills it.
- A record's order is per partition and nowhere else; a producer wanting a
  total order uses one partition, and says so in its policy.

## Amendment — the file engine (#821), 2026-11-12

`NewFile(FileConfig{Clock, Dir, Policy})` keeps a stream in a directory:
`p<partition>/`, one `kernel/fs/seglog` log each, and `cursors/`, one file
per group and partition published by `vfs.WriteAtomic`. Decided with it:

1. **One batch, one sync.** An `Append` is one seglog batch — its records,
   and a drop record when retention moved — under one `Sync`: two
   fdatasyncs whatever the batch's size, so a crash keeps all of it or none.
2. **Every bound acts on the first offset at once; the storage follows in
   whole segments.** A record past `RetainFor`, beyond `RetainBytes` or
   below a `Trim` is unreadable from that instant (`OFFSET_TRIMMED`), as on
   the memory engine, so the conformance suite holds both to the same
   cases. The `Append` or `Trim` that moves the first offset writes it as a
   drop record in its batch, and seglog unlinks every sealed segment that
   then holds nothing readable after that sync — a segment older than
   `RetainFor` goes at the first `Append` that finds it expired, the one
   being written at the next rotation. The age bound is computed at read
   time, as the memory engine's is.
3. **The index is in memory and rebuilt at open.** Every record carries its
   batch's instant (8 bytes) and its key's length before the key and the
   payload; the head of a batch is tagged by seglog's kind byte. An open
   reads the log once and keeps, per partition, the prefix sums of the
   records' bytes and each batch's first offset and instant: `Bounds`,
   `OffsetAt` and every bound are binary searches with no read of the
   device, and a `Read` is one `pread` per contiguous run.
4. **One engine per directory.** A segmented log has one writer, so the
   engine holds an exclusive `kernel/fs/flock` lock on `<Dir>/lock` until
   `Close`: a second open, in this process or another, is
   `STREAM_DIRECTORY_UNUSABLE` (`why=in-use`), and a platform with no file
   lock is refused the same way (`why=no-file-lock`). The port's "every
   process that opens the same storage" is honoured by refusal, not by
   sharing; the Waker of the open engine is therefore the directory's, and
   the queue's process-wide wake table is not needed. The groups' cursors
   are published through `service/data/vfs`'s disk filesystem, so the file
   engine runs where that filesystem exists: on Windows, illumos and
   Solaris, which `vfs.NewOS` refuses (ADR 0018: no flushable directory
   handle, a mode that is not an access rule), `NewFile` refuses with
   `UNSUPPORTED_PLATFORM` and the memory and SQL engines remain. Its
   suites assert that refusal there, then skip (ADR 0095).
5. **The directory rules are the queue's**: the root refused world-writable
   without the sticky bit, every subdirectory refused world-writable at all,
   a link or a non-directory refused by shape, the DACL read on Windows.
6. **A partition count cannot shrink**: a directory holding `p<N>` with N at
   or past `Partitions` is `STREAM_MISCONFIGURED` (`field=Partitions`).

## Amendment — the SQL engine (#822), 2026-11-12

`NewSQL(SQLConfig{Transactor, Dialect, Table, Policy, Clock})` keeps a
stream in three tables of the caller's database, created by
`SQLMigration(dialect, table, version)`: `<table>` (one row per record, keyed
`(part, record_offset)`), `<table>__heads` (per partition the next offset,
the floor a `Trim` raised and the last batch's instant) and
`<table>__cursors` (per group digest and partition the next offset). Decided
with it:

1. **ADR 0151's pattern, unchanged.** The transactor must be a `Joiner` and
   a `Deferrer`; inside the caller's transaction an `Append`, a `Trim` and a
   `Commit` are savepoints of it, outside one an `Append` or a `Trim` runs in
   a transaction of the engine's own (READ COMMITTED on MySQL) and a `Commit`
   on the pool; the `Waker` fires through `Defer`, after the commit; a
   driver's error is joined under `Withheld`, never rendered. A `Commit` on a
   handler's own transaction's context commits the handler's effect and the
   group's offset together — exactly-once in effect (#823 builds on it).
2. **A partition's appenders are serialised on its head row**, so offsets
   commit in order and no reader can pass a record that commits later. The
   first statement of an `Append` is an upsert that creates the head row or
   writes it back unchanged — the row lock on PostgreSQL and MySQL (an
   exclusive lock, so the head's first creation by racing appenders does not
   deadlock), and on SQLite the write that takes the database's lock before
   anything is read (ADR 0140) —, then the head is read `FOR UPDATE`, the
   records are inserted 199 per statement (995 parameters, under SQLite's
   999) and the head is advanced: 3 + chunks statements. Appenders of
   different partitions do not wait on one another.
3. **A `Read` is one unlocked statement**: the head row LEFT JOINed with the
   readable records, so the next offset and the records arrive together and
   `OFFSET_TRIMMED` / `OFFSET_AHEAD` need no second round trip.
4. **Every bound acts on the first offset at once; the rows follow.** A
   `Trim` raises the head's floor in one statement under its lock, then
   deletes the rows below in statements of at most 1 024 offsets. The age
   bound is read-time, `appended_at` after `now − RetainFor` on the engine's
   clock, never the database's; an `Append` deletes the expired rows of its
   partition at most once per `min(RetainFor, 1 min)`.
5. **No byte bound.** The tables keep no count of a partition's bytes, so a
   `RetainBytes` or a `MaxBytes` is refused at construction
   (`SQL_STREAM_MISCONFIGURED`) rather than ignored; the conformance suite
   checks the refusal and skips those cases for the SQL rows.
6. **A table name is lower-case words joined by single underscores**, at
   most 54 bytes (PostgreSQL's 63 less `__cursors`): with no double
   underscore, no stream's table is another stream's derived one. A group's
   cursor is keyed by the SHA-256 of its name, the name kept beside it.

## Amendment — the subscriber (#823), 2026-11-12

`Subscribe(ctx, stream, SubscriberConfig)` reads a stream for one consumer
group, modelled on `queue.Consume`. Decided with it:

1. **One worker per assigned partition, in a joined group.** Each reads its
   partition in offset order from the group's committed offset — or, for a
   group new to the partition, from its first retained offset; a cursor
   retention passed resumes at the first kept. The stream's `Waker` signal
   is taken before each `Read`, the idle wait is bounded by `PollInterval`
   on the injected clock. Assignment is static (`Partitions`, empty for
   every one): a rebalance needs membership, which this domain leaves to
   consensus.
2. **A refused batch blocks its partition, and only it.** It is retried on
   the `kernel/backoff` curve (`Retry`, 100 ms doubling to 30 s when zero),
   so the records of one key keep their order — the per-key ordering ADR 0054
   deferred, with its head-of-line blocking stated. With `Park`, a batch
   refused `After` times is narrowed to one record at a time, and a record
   refused `After` times alone is published to a `queue.Broker` as the JSON
   of its `EntryValue` and committed past. A handler's panic is a refusal.
3. **Two commit modes.** `CommitAfterHandler`, the zero: the subscriber
   commits the offset after a batch the handler accepted — at least once on
   every engine, a crash between the two delivering the batch again.
   `CommitByHandler`: the handler commits through the stream's `Committer`
   inside the transaction holding its effect — on the SQL engine, where an
   Append and a Commit on a transaction's context are savepoints of it
   (amendment #822), exactly once in effect; on memory and file, at least
   once, the cursor durable after the handler. `HandlerIsIdempotent` is
   required in both, as the queue requires it.
4. **What a stop leaves.** The handler's context is cancelled; a batch
   accepted as the stop lands is still committed, a dead letter still
   published and a fence still released, on the context without its
   cancellation and bounded by `SettleTimeout` (5 s) on the clock.
5. **An optional fence.** With a `core/app/lock.Locker`, each worker holds
   `stream:<Name>:<Group>:<partition>` while it reads (`TryAcquire`, a
   standby of `PollInterval` when held elsewhere), renews an expiring lease
   a third of its life on and before every commit it makes, and gives the
   partition up uncommitted on a refused renewal. The stores do not check the
   fencing token, so the fence narrows a double reader's window and does not
   close it.
6. **No new code.** A configuration the subscriber cannot run is
   `SUBSCRIBER_MISCONFIGURED` (`0.3.94.4`, allocated with the domain), its
   `field` naming the value; a medium's refusal — the stream's, the park's
   queue's, the locker's — is returned as it came and stops every worker.

## Amendment — compaction by key (#824), 2026-11-12

`Compactor.Compact(ctx, partition) (CompactValue{Scanned, Kept, Dropped},
error)` on every engine, under a new policy field `PolicyValue.CompactAfter`
(zero never compacts, negative refused — one reading of the zero, ADR 0031).
It resolves what this ADR deferred to #824:

1. **What goes.** A record is compactable once it is `CompactAfter` old; one
   younger is never touched, so a reader within that window of the head sees
   every update. Among the compactable records, the last of each key stays
   and every earlier one goes; an unkeyed record stays. The comparison is the
   service's one function (`compactLines`), so every engine drops the same
   records.
2. **Tombstones.** A keyed record with an empty payload is a tombstone. It
   stays, as its key's last record, until it is twice `CompactAfter` old —
   one window to become compactable, one more for a reader bootstrapping
   from offset 0 to learn the deletion — and the first compaction after goes
   with it. No state is kept for that: the record's instant is the clock.
3. **Offsets keep, holes are skipped, `Bounds` does not move.** A read from
   inside a compacted range returns the next survivor; `OFFSET_TRIMMED`
   stays a read below the first retained offset and nothing else, so
   `BoundsValue.First` and `OffsetTrimmed` no longer name compaction.
4. **Nothing runs on its own.** No timer, no goroutine: a caller runs
   `Compact`, from a scheduler's job — the queue's rule.
5. **Per engine.** Memory: in place, under the stream's lock. File: the
   decision read from the log (a key map over the compactable range), then
   `seglog.Log.Rewrite` (ADR 0230's amendment) replaces each dropped record
   by its batch's instant alone — eight bytes no encoded record can have —,
   so the index, the batches and the age bound are unchanged and a reopen
   reads the hole; the freed bytes are released under `MaxBytes`. SQL: the
   compactable rows read a page of 1 024 at a time (offset, key, instant,
   whether the payload is empty — never the payload), the decision made in
   the engine, the dropped rows deleted by offset 256 a statement, each a
   savepoint of the caller's transaction or a statement of its own, and a
   row a delete finds aged out left to the age sweep.
6. **The SQL engine's first offset no longer reads the lowest row** (the
   gap #822 flagged): it is the head's floor or the offset after the highest
   stored row past the age bound, whichever is higher. The instants follow
   the offsets, so a compaction's holes never move it; the age sweep now
   raises the floor, as a `Trim` does, before it deletes the expired rows,
   so their going does not move it either.

