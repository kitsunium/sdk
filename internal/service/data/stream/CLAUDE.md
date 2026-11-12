# internal/service/data/stream/

## Purpose

The engines of `internal/core/data/stream`'s `Stream` port (ADR 0231) and the
conformance suite every one of them runs. Today: the memory engine,
`NewMemory` (#819), and the file engine, `NewFile`, a directory of segments
over `kernel/fs/seglog` with retention by age and bytes, `Trim` and
`OffsetAt` (#821, ADR 0231's amendment), and the SQL engine, `NewSQL`, three
tables of the caller's database joined to the transaction its context
carries on ADR 0151's pattern (#822, ADR 0231's second amendment). To come:
the subscriber that reads a stream for a consumer group (#823).

## Surface

| Symbol | What it is |
|---|---|
| `NewMemory(MemoryConfig) (corestream.Stream, error)` | the in-process engine, the test double of every durable one; also a `Waker`, `Seeker`, `Trimmer` and `Committer` (not a `Compactor`: #824) |
| `NewFile(FileConfig) (corestream.Stream, error)` | the durable engine: `<Dir>/p<partition>/` one segmented log each, `<Dir>/cursors/` one file per group and partition; also a `Waker`, `Seeker`, `Trimmer`, `Committer` and `io.Closer`, the directory locked until `Close` |
| `NewSQL(SQLConfig) (corestream.Stream, error)` | the engine over `<Table>`, `<Table>__heads` and `<Table>__cursors`: an `Append` or a `Commit` inside the caller's transaction is a savepoint of it, the `Waker` fires through `Deferrer` after its commit; also a `Waker`, `Seeker`, `Trimmer` and `Committer`; a `RetainBytes` or `MaxBytes` is refused (`SQL_STREAM_MISCONFIGURED`) |
| `SQLConfig{Transactor, Dialect, Table, Policy, Clock}` | the transactor (a `Joiner` and a `Deferrer`), the dialect, the table (lower-case words joined by single underscores, at most 54 bytes, not `sqlite_…`), the policy and the engine's clock (`clock.Clock`, nil: `clock.System`) |
| `SQLMigration(dialect, table, version) (coresql.MigrationValue, error)` | the three `CREATE TABLE IF NOT EXISTS`, checksummed; Down drops them |
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
| `sql_config.go` | `SQLConfig`, its validation (`validateSQLNames`, `sqlMisconfigured`), `sqlClockOrSystem` |
| `sql.go` | `sqlStream`, `NewSQL`, the process's wake table, `Partitions`, `Append` (`write`), `run`, `announce`, `failed`, the age `cutoff` |
| `sql_read.go` | `Read` (one statement), `Bounds`, `OffsetAt`, `Wake` |
| `sql_retain.go` | `Trim` (`raiseFloor`, then `release` in bounded DELETEs) and the age `sweep` an Append runs at most once a period |
| `sql_commit.go` | `Commit`, `Committed`: the cursor keyed by the SHA-256 of its group |
| `sql_dialect.go` | every statement, rendered once per dialect; the DDL and `SQLMigration` |
| `sql_compliance.go` | the compile-time assertions of the port and its four siblings |
| `batch.go` | what every engine checks before it writes (`admit`, `keyedPartition`) and the byte copies (`carve`, `copyEntries`) |
| `errors.go` | the core's sentinels wrapped with their fields — no code is declared here |
| `wake.go` | `wakeSignal`, the queue's broadcast without its due instant |
| `harness_external_test.go` | `everyStream`, the table of engines, and the helpers the suite shares |
| `conformance_external_test.go` | the cases every engine runs, each over `everyStream` |
| `memory_external_test.go` | what only the memory engine has: its construction |
| `sqlfake_external_test.go`, `sql_export_test.go` | the fake SQL engine — a `database/sql` driver keeping the three tables in maps, the queue's fake for these tables — and the rendered statements it recognises |
| `sql_external_test.go` | what only the SQL engine has: an Append that exists if and only if its transaction commits, a failed one leaving the caller's transaction usable with the driver's words withheld, offsets committed in order under concurrent appenders some of which roll back, the statements per call (`Append` = 3 + one per chunk of 199, `Read` = 1, `Commit` = 2), a Commit in the handler's transaction, Trim and the age sweep deleting rows, the refusals, the statement texts pinned |
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
`everyStream` — a name and a factory over `(t, clock.Timed, PolicyValue)`. The
SQL engine is three rows, one per dialect, over the fake engine; its factory
skips a case whose policy names a byte bound after checking the refusal. A
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

- **The SQL engine serialises the appenders of a partition on its head
  row.** An Append's first statement is an upsert that creates the head row
  or writes it back unchanged — the row lock on PostgreSQL and MySQL, the
  write that takes SQLite's lock first (ADR 0140) — held until the
  transaction ends, so offsets are assigned in commit order and a reader
  following a partition never passes a record that commits later. Then the
  head is read (`FOR UPDATE`), the records inserted 199 per statement, the
  head advanced: 3 + chunks statements. A rolled-back Append takes its
  offsets back with it.
- **The SQL engine's Read is one unlocked statement**: the head row LEFT
  JOINed with the readable records. The records are stored contiguously from
  the head's floor and stamped in offset order, so a first row past `from`
  means `from` was trimmed (#824 must revisit this once compaction leaves
  gaps).
- **The SQL engine's Trim moves a floor, then deletes.** The head's
  `first_offset` is raised in one statement under the head's lock, so the
  records below are unreadable at once; the rows go in DELETEs of at most
  1 024 offsets. Age retention is read-time (`appended_at > now - RetainFor`)
  and its rows are deleted by an Append at most once per `min(RetainFor,
  1 min)` per partition and engine — a failed sweep is retried by the next.
- **The SQL engine keeps no byte count**: `RetainBytes` and `MaxBytes` are
  refused at construction rather than ignored.
- **A group's cursor is keyed by the SHA-256 of its name**, so no group is
  too long for a key; the name is kept beside it for an operator.

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
