# ADR 0231 — a record is kept until its retention ends, and every group reads it at its own pace

- **Status**: Accepted
- **Date**: 2026-11-12
- **Deciders**: SDK maintainers
- **Amended**: 2026-11-12 for the file engine (#821), below.
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
   the queue's process-wide wake table is not needed.
5. **The directory rules are the queue's**: the root refused world-writable
   without the sticky bit, every subdirectory refused world-writable at all,
   a link or a non-directory refused by shape, the DACL read on Windows.
6. **A partition count cannot shrink**: a directory holding `p<N>` with N at
   or past `Partitions` is `STREAM_MISCONFIGURED` (`field=Partitions`).
