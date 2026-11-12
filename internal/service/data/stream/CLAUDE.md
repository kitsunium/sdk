# internal/service/data/stream/

## Purpose

The engines of `internal/core/data/stream`'s `Stream` port (ADR 0231) and the
conformance suite every one of them runs. Today: the memory engine,
`NewMemory` (#819), and the file engine, `NewFile`, a directory of segments
over `kernel/fs/seglog` with retention by age and bytes, `Trim` and
`OffsetAt` (#821, ADR 0231's amendment). To come, one milestone each of
#890: a SQL table joined to the caller's transaction on ADR 0151's pattern
(#822), and the subscriber that reads a stream for a consumer group (#823).

## Surface

| Symbol | What it is |
|---|---|
| `NewMemory(MemoryConfig) (corestream.Stream, error)` | the in-process engine, the test double of every durable one; also a `Waker`, `Seeker`, `Trimmer` and `Committer` (not a `Compactor`: #824) |
| `NewFile(FileConfig) (corestream.Stream, error)` | the durable engine: `<Dir>/p<partition>/` one segmented log each, `<Dir>/cursors/` one file per group and partition; also a `Waker`, `Seeker`, `Trimmer`, `Committer` and `io.Closer`, the directory locked until `Close` |
| `FileConfig{Clock, Dir, Policy}` | the clock that stamps and ages, the stream directory (the queue's directory rules), and the policy, refused at construction by the core's `Validate` and by a `MaxRecordBytes` one segment record cannot hold |
| `MemoryConfig{Clock, Policy}` | the `clock.Timed` that stamps, ages and times the backpressure wait (nil: `clock.System`), and the `corestream.PolicyValue`, refused at construction by the core's `Validate` |

## Contents

| File | What it holds |
|---|---|
| `doc.go`, `gen_api_test.go` | written by kit from `design/data/stream.yaml`: the package comment and the pins |
| `memory_config.go` | `MemoryConfig`, `clockOrSystem` |
| `memory.go` | `memoryStream`, `memPartition`, `NewMemory`, `Partitions`, `Append` and the batch's store |
| `memory_read.go` | `Read`, `Bounds`, `OffsetAt`, `Wake`, the lazy age retention (`retained`, `expired`) |
| `memory_retain.go` | the `MaxBytes` wait (`reserve`, `await`), `RetainBytes`, the removal of a prefix (`drop`), `Trim` |
| `memory_commit.go` | `Commit`, `Committed` |
| `memory_compliance.go` | the compile-time assertions of the port and its four siblings |
| `file_config.go` | `FileConfig`, the directory's preparation and checks (`prepareRoot`, `prepareSub`), `backendFailed`, `directoryUnusable` |
| `dirtrust_posix.go`, `dirtrust_windows.go` | the root's and the subdirectories' permission rules — the mode bits, or the DACL through `kernel/fs/winacl` — copied from the queue |
| `file.go` | `fileStream`, `NewFile`, the lock, the partition count check, `Append` (`write`, `syncBatch`, `release`), `stamp`, `Close` |
| `file_part.go` | `filePart`: the per-partition index (prefix sums of bytes, batches' first offsets and instants, `kept`), `first`, `bounded`, `claim`, `offsetAt`, `rebuild` at open |
| `file_record.go` | a record's segment payload: its batch's instant, its key's length, the key, the payload; the head and tail kind bytes |
| `file_read.go` | `Read` (one `Range`), `Bounds`, `OffsetAt`, `Wake` |
| `file_retain.go` | the `MaxBytes` wait (`reserve`, `expireAll`, `untilExpiry`, `await`) and `Trim` |
| `file_commit.go` | `Commit`, `Committed`: `cursors/<digest of the group>.<partition>`, the offset then the group |
| `file_compliance.go` | the compile-time assertions of the port, its four siblings and `io.Closer` |
| `batch.go` | what every engine checks before it writes (`admit`, `keyedPartition`) and the byte copies (`carve`, `copyEntries`) |
| `errors.go` | the core's sentinels wrapped with their fields — no code is declared here |
| `wake.go` | `wakeSignal`, the queue's broadcast without its due instant |
| `harness_external_test.go` | `everyStream`, the table of engines, and the helpers the suite shares |
| `conformance_external_test.go` | the cases every engine runs, each over `everyStream` |
| `memory_external_test.go` | what only the memory engine has: its construction |
| `file_external_test.go` | what only the file engine has: its refusals, the lock, a reopen keeping records, trim, cursors and the byte bound, a segment past `RetainFor` unlinked |
| `death_external_test.go` | the SIGKILL test: a child appending numbered batches is killed, and every batch its `Append` acknowledged is read back whole, none in part (the queue's victim pattern, `KTNS_VICTIM_STREAM_DIR`) |
| `stream_bench_test.go`, `BENCH.md` | Append (1/16/256 records at 64 B and 4 KiB), Read (1/16), `PartitionOf`, keyed against unkeyed; the file engine's Append (1/16/256) and Read of 16 at a 1 000 and a 1 000 000-record backlog |

## The conformance suite, and adding an engine

Every case in `conformance_external_test.go` ranges over `everyStream()`
through `forEachStream`, on a `clock.ManualClock` of its own: contiguous
offsets per partition from zero, the empty read at the next offset,
`OFFSET_TRIMMED` and `OFFSET_AHEAD`, per-key placement in append order, unkeyed
batches in turn, `PARTITIONS_MIXED`, `RECORD_TOO_LARGE` before anything is
written, `INVALID_BATCH_SIZE`, `UNKNOWN_PARTITION` on every call, Trim then
Bounds, the Commit/Committed round trip and `UNKNOWN_GROUP`, the Waker closed
by an Append, `OffsetAt`, retention by age and by bytes, `STREAM_FULL` at once
and after `BlockTimeout` and the wait a Trim ends, the engine's own copy of
every byte, and concurrent appends. An engine joins with ONE row of
`everyStream` — a name and a factory over `(t, clock.Timed, PolicyValue)`. A
case about a sibling takes it through `sibling[T]`, which skips on an engine
without it: a sibling is optional (ADR 0039).

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

## Do NOT

- Declare an error code here (ADR 0160): `STREAM_BACKEND_FAILED`,
  `STREAM_DIRECTORY_UNUSABLE`, `SQL_STREAM_MISCONFIGURED` and
  `SUBSCRIBER_MISCONFIGURED` are `core/data/stream`'s; a new one goes in the
  design's `0.3.94.*` block.
- Place a key on a partition any other way than `core/data/stream.PartitionOf`.
- Write a conformance case for one engine only: it goes over `everyStream`, or
  in that engine's own `*_external_test.go` when it is truly its alone.
- Import `internal/service/app/consensus`: data sits below app (ADR 0155); the
  segmented log is the kernel's (`kernel/fs/seglog`, ADR 0230).
