# internal/service/data/stream — notes

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles.

## Contents

| File | What it holds |
|---|---|
| `doc.go`, `gen_api_test.go` | written by kit from `design/data/stream.yaml`: the package comment and the pins |
| `memory_config.go` | `MemoryConfig`, `clockOrSystem` |
| `memory.go` | `memoryStream`, `memPartition`, `NewMemory`, `Partitions`, `Append` and the batch's store |
| `memory_read.go` | `Read`, `Bounds`, `OffsetAt`, `Wake`, the lazy age retention (`retained`, `expired`) |
| `memory_retain.go` | the `MaxBytes` wait (`reserve`, `await`), `RetainBytes`, the removal of a prefix (`drop`), `Trim` |
| `memory_commit.go` | `Commit`, `Committed` |
| `memory_compact.go` | `Compact`: survivors slid down in place, the freed bytes released under `MaxBytes` |
| `compact.go` | what every engine decides by: `compactLines` (the compactable instant and the tombstone's), `drops`, `compactResult` |
| `memory_compliance.go` | the compile-time assertions of the port and its five siblings |
| `file_config.go` | `FileConfig`, the directory's preparation and checks (`prepareRoot`, `prepareSub`), `backendFailed`, `directoryUnusable` |
| `dirtrust_posix.go`, `dirtrust_windows.go` | the root's and the subdirectories' permission rules — the mode bits, or the DACL through `kernel/fs/winacl` — copied from the queue |
| `file.go` | `fileStream`, `NewFile`, the lock, the partition count check, `Append` (`write`, `syncBatch`, `release`), `stamp`, `Close` |
| `file_part.go` | `filePart`: the per-partition index (prefix sums of bytes, batches' first offsets and instants, `kept`), `first`, `bounded`, `claim`, `forgetBelow`, `offsetAt`, `rebuild` at open, `forget` (a compaction's dropped bytes) |
| `file_record.go` | a record's segment payload: its batch's instant, its key's length, the key, the payload; the head and tail kind bytes; a compacted record's payload, the instant alone (`encodeGone`, `isGone`, `decodeStored`) |
| `file_read.go` | `Read` (one `Range` a round, holes skipped), `Bounds`, `OffsetAt`, `Wake` |
| `file_compact.go` | `Compact`: the plan (each key's last compactable offset) read from the log, then `seglog.Rewrite` replacing each dropped record by its instant; `youngFrom` |
| `file_retain.go` | the `MaxBytes` wait (`reserve`, `expireAll`, `untilExpiry`, `await`) and `Trim` |
| `file_commit.go` | `Commit`, `Committed`: `cursors/<digest of the group>.<partition>`, the offset then the group |
| `file_compliance.go` | the compile-time assertions of the port, its five siblings and `io.Closer` |
| `sql_config.go` | `SQLConfig`, its validation (`validateSQLNames`, `sqlMisconfigured`), `sqlClockOrSystem` |
| `sql.go` | `sqlStream`, `NewSQL`, the process's wake table, `Partitions`, `Append` (`write`), `run`, `announce`, `failed`, the age `cutoff` |
| `sql_read.go` | `Read` (one statement), `Bounds` (`boundsAt`, `firstReadable`), `OffsetAt`, `Wake` |
| `sql_compact.go` | `Compact`: compactable rows read a page at a time, the decision in the engine, DELETEs by offset |
| `sql_retain.go` | `Trim` (`raiseFloor`, then `release` in bounded DELETEs) and the age `sweep` an Append runs at most once a period, a `Trim` to the first readable offset |
| `sql_commit.go` | `Commit`, `Committed`: the cursor keyed by the SHA-256 of its group |
| `sql_dialect.go` | every statement, rendered once per dialect; the DDL and `SQLMigration` |
| `sql_compliance.go` | the compile-time assertions of the port and its five siblings |
| `batch.go` | what every engine checks before it writes (`admit`, `keyedPartition`) and the byte copies (`carve`, `copyEntries`) |
| `subscribe_config.go` | `CommitMode` and its two values, `Handler`, `ParkConfig`, `SubscriberConfig`, its `validate`, `missing` and `normalized` |
| `subscribe.go` | `Subscribe`, `subscriber`, `cursor`, a worker's `run`, `follow`, `start`, `turn`, `settle`, `handle` (a panic is a refusal), `advance` (the commit), `sleep`, `settled` and `bound` (an outcome past the stop) |
| `subscribe_fence.go` | `heldFence`: the lock's name, `acquire` (`TryAcquire`), `renew` (a third of the lease's life), `bounded` (a wait never past a renewal), `release` |
| `subscribe_park.go` | `narrowOrPark` and `park`: a refused batch narrowed to one record, a refused record published as its entry's JSON and committed past |
| `errors.go` | the core's sentinels wrapped with their fields — no code is declared here |
| `wake.go` | `wakeSignal`, the queue's broadcast without its due instant |
| `harness_external_test.go` | `everyStream`, the table of engines, and the helpers the suite shares |
| `conformance_external_test.go` | the cases every engine runs, each over `everyStream` |
| `compact_external_test.go` | the compaction cases every engine runs: a key's last compactable record and every young one kept at their offsets, `Bounds` unmoved, a read inside a hole; a tombstone kept one window then gone with its key, `OFFSET_TRIMMED` only below the first offset; a young record untouched; a zero `CompactAfter` |
| `memory_external_test.go` | what only the memory engine has: its construction |
| `sqlfake_external_test.go`, `sql_export_test.go` | the fake SQL engine — a `database/sql` driver keeping the three tables in maps, the queue's fake for these tables — and the rendered statements it recognises |
| `sqllocks_external_test.go` | the fake's concurrent mode: transactions side by side over the committed tables plus their own writes, the head's row locks, and on MySQL InnoDB's locks on a missing key (§The head on MySQL); with `rowLocks` false, the negative control |
| `sql_external_test.go` | what only the SQL engine has: an Append that exists if and only if its transaction commits, a failed one leaving the caller's transaction usable with the driver's words withheld, offsets committed in order under concurrent appenders some of which roll back — on the serialising and the concurrent fake, with the control that the race fails on one without locks —, a fresh partition outliving its first appender's rollback, the MySQL head created on a second connection, the statements per call (`Append` = 3 + one per chunk of 199, 2 + chunks on MySQL once the head is known, `Read` = 1, `Commit` = 2), a Commit in the handler's transaction, Trim and the age sweep deleting rows, the refusals, the statement texts pinned |
| `file_external_test.go` | what only the file engine has: its refusals, the lock, a reopen keeping records, trim, cursors and the byte bound, a segment past `RetainFor` unlinked |
| `death_external_test.go` | the SIGKILL test: a child appending numbered batches is killed, and every batch its `Append` acknowledged is read back whole, none in part (the queue's victim pattern, `KTNS_VICTIM_STREAM_DIR`, dropped from the inherited environment before the child's is set; a victim spawns none, and stops by itself after 20 000 batches or two minutes when its parent is gone) |
| `subscribe_external_test.go` | the subscriber over `everyStream`: two groups each receiving every record once, per-key order over four partitions and a failing partition blocking only itself, a crash before the commit redelivering, a record parked as JSON, the fence keeping one reader per partition, the refusals |
| `subscribe_sql_external_test.go` | exactly-once in effect on the SQL engine: the handler's effect (a record on partition 1) and its Commit in one transaction failed after the write then committed, one effect after a restart; and the contrast, `CommitAfterHandler` applying the effect twice |
| `subscribe_bench_test.go` | `BenchmarkSubscribe`: a record delivered and committed at batches of 1, 16 and 256 |
| `compact_bench_test.go` | `BenchmarkCompact`: 4 096 records over 64 keys compacted, memory and file |
| `stream_bench_test.go`, `BENCH.md` | Append (1/16/256 records at 64 B and 4 KiB), Read (1/16), `PartitionOf`, keyed against unkeyed; the file engine's Append (1/16/256) and Read of 16 at a 1 000 and a 1 000 000-record backlog |

## The conformance suite, and adding an engine

Every case in `conformance_external_test.go` ranges over `everyStream()`
through `forEachStream`, on a `clock.ManualClock` of its own: contiguous
offsets per partition from zero, the empty read at the next offset,
`OFFSET_TRIMMED` and `OFFSET_AHEAD`, per-key placement in append order, unkeyed
batches in turn, `PARTITIONS_MIXED`, `RECORD_TOO_LARGE` before anything is
written, `INVALID_BATCH_SIZE`, `UNKNOWN_PARTITION` on every call, Trim then
Bounds, the Commit/Committed round trip and `UNKNOWN_GROUP`, the Waker closed
by an Append, `OffsetAt`, retention by age and by bytes, compaction by key, `STREAM_FULL` at once
and after `BlockTimeout` and the wait a Trim ends, the engine's own copy of
every byte, and concurrent appends. An engine joins with ONE row of
`everyStream` — a name and a factory over `(t, clock.Timed, PolicyValue)`. The
SQL engine is three rows, one per dialect, over the fake engine; its factory
skips a case whose policy names a byte bound after checking the refusal. A
case about a sibling takes it through `sibling[T]`, which skips on an engine
without it: a sibling is optional (ADR 0039).

A fourth engine lives outside this package and is not a row of
`everyStream`: `internal/service/app/consensus.NewStream` (#832) presents a
consensus node as a stream of one partition whose offsets are the log's
indexes — not contiguous, the group's other entries skipped, not from zero —,
retained by the node's compaction (no `Trimmer`, `Seeker`, `Compactor` or
`Committer`), one entry per record so a batch is not atomic. The suite's
offset and retention cases cannot hold for it, and `data` must not import
`app`; its contract cases run in that package on the memory cluster of three
nodes (`stream_internal_test.go`).

## The head on MySQL (#822)

The audit of lot L1 (#1361) ran `e2e/integration/sql`'s
`TestConcurrentAppendersCommitInOrderOnEveryEngine` on MySQL: sixteen
appenders of a partition never written failed `STREAM_BACKEND_FAILED`, then
`SAVEPOINT_FAILED` (error 1305). The head's upsert, `INSERT … ON DUPLICATE
KEY UPDATE`, sent by several transactions on a missing key, does this under
InnoDB (MySQL 8.0 manual, 17.7.3 "Locks Set by Different SQL Statements",
17.7.1 "InnoDB Locking", 17.7.2.1, 17.7.5.2, 17.21.5, 15.3.4):

- the first inserts the row and holds its exclusive record lock; the others
  hit the duplicate and queue for a lock on that record;
- the first rolls back: the row is gone, and the waiters' locks stay on the
  gap where it was — gap locks coexist, are "purely inhibitive", and READ
  COMMITTED keeps them for duplicate-key checking;
- each waiter now inserts the key, and its insert intention waits for the
  gap the others hold: a cycle, and InnoDB rolls "the entire transaction"
  of one back; the transactor's `ROLLBACK TO SAVEPOINT` then answers 1305,
  the savepoint gone with it.

So on MySQL the head is created by `INSERT IGNORE`, autocommit on the pool,
which never rolls back, and every Append locks the committed row by the
head's read `FOR UPDATE`. Inside a caller's transaction the creation comes
after an unlocked read found no row — a locking read of a missing row would
take a gap lock under REPEATABLE READ that the creation, on another
connection, would wait on — and takes a second connection of the pool once
per partition: a pool whose every connection such transactions hold waits
for one until the context ends (`TestSQLAMySQLHeadIsCreatedOnASecondConnection`).
`heads` remembers, per partition, that the row exists, and a failed head read
forgets it.

The serialising fake could not fail the ordering test. The concurrent fake
models the list above for MySQL — `release` copies a rolled-back fresh
row's waiters into its gaps, `acquire` kills the transaction whose insert
closes the cycle, `errDeadlock` and the savepoints gone — and nothing more:
PostgreSQL's waiter inserts the key once the first rolls back, and an
autocommit statement holds its lock alone. On main before the fix,
`TestSQLAFreshPartitionOutlivesItsFirstAppenderRollingBack/mysql` failed
30 of 30 (STREAM_BACKEND_FAILED with SAVEPOINT_FAILED in its chain, one
transaction rolled back on the head), and the ordering race on it 8 of 30 —
only when its first appender is one of the two that roll back their first
transaction. The real engine stays the proof: no CI job runs
`e2e/integration/sql` on MySQL yet (#822's option (a)).
