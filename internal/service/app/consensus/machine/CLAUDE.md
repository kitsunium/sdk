<!-- updated: 2026-11-12T12:00:00Z -->
# internal/service/app/consensus/machine/

## Purpose

State machines for the consensus engine (ADR 0152 D3, #916), published by
`pkg/v1/app/consensus/machine`:

- `SQL` (`NewSQL`, `SQLConfig`) — a `DurableStateMachine` and a
  `BatchingStateMachine` over a `core/data/sql.Transactor`. A batch is one
  transaction: the caller's `Apply` per command, then the UPDATE of the one
  position row (`DefaultPositionTable`, id 1: the log identity's six fields
  and the applied term and index), then the commit. A snapshot is a
  read-only transaction (REPEATABLE READ, SERIALIZABLE on SQLite) opened and
  read once on the apply goroutine, held by a goroutine until `Release`,
  `WriteTo` served in it (`sqlsnap.go`); a restore is `Truncate`, `Load`
  and the snapshot's position in one transaction. Snapshot format: `KSQL`,
  version 1, then what `Dump` wrote.
- `KV` (`NewKV`, `KVGet`, `KVPut`, `KVCompareAndSwap`, `KVResult`) — the
  reference volatile machine: a per-key revision that only grows (D6's ABA
  defence), a command it does not read answered a VERSION_UNSUPPORTED error.
  Snapshot format: `KKV`, version 1, then the keys in order.

It declares no code: it raises the core consensus sentinels
(CONSENSUS_MISCONFIGURED, STATE_MACHINE_FAILED, SNAPSHOT_CORRUPT,
VERSION_UNSUPPORTED). It imports `core/app/consensus` and `core/data/sql`,
never `service/app/consensus`, so the engine's own tests may use it.

## Tests

- `sqldouble_external_test.go` — the SQL double: a database/sql driver over
  memory, snapshot isolation by copy, that knows the position table and the
  tests' `entries` table and fails any other statement; `armCrash` fails the
  next position write at or past an index, once.
- `cluster_external_test.go` — three nodes over the memory transport, built
  through the engine's published API; the crash between a batch and its
  position (`TestADurableSQLMachineCrashedBeforeItsPositionAppliesEveryEntryOnce`).
- `lincheck_external_test.go` — the raft simulator's linearizability
  checker, repeated (a test file cannot be imported), its known histories,
  and a run of four clients over the KV with a leader crash, the history
  mutated as its negative control.

## Do NOT

- Write the position outside the batch's transaction: a crash between the
  two replays the batch or skips it, the defect this package exists for.
- Recover a failed batch: it panics, the node stops STATE_MACHINE_FAILED
  (D3), and the restart replays from the position the database kept.
- Hand-edit the doc comments: they are `design/app/consensus.yaml`'s.
