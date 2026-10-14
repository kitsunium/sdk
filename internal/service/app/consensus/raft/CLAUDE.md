<!-- updated: 2026-10-14T16:40:00Z -->
# internal/service/app/consensus/raft/

## Purpose

The step function of ADR 0152 (D15): no goroutine, no I/O, no clock. Every
decision of the protocol is here; every effect leaves through `Ready`.
Durability is an input (`StorageDone`), and the replies that depend on a
write leave in `Ready.AfterSync`.

## Tests

The deterministic simulator (`sim_internal_test.go`), the constructed checks
and `TestMutationsAreCaught` (one mutation per cited bug, behind the
unexported `Config.mut`), and the linearizability checker with its negative
control (`lincheck_internal_test.go`).

The suites cost ~300 CPU-seconds under race and coverage against a 60 s cap
per target, so `raft_test` has 26 shards and Bazel deals them TOP-LEVEL tests:
a seed suite is split into top-level parts (`seedPart`) and a loop of seeds
runs them as parallel subtests (`eachSeed`) — never fewer seeds (#126, #304).
A new heavy suite follows the same shape; measure every shard side by side
under coverage before touching `shard_count`.

## Do NOT

- Iterate a map where the order reaches a message: a run must stay a pure
  function of its seed.
- Acknowledge a snapshot install past its index (review H1,
  `mutSnapshotAckLastIndex`).
- Append, acknowledge or skip an entry kind `Known` refuses: report it in
  `Ready.Unsupported` (review M3).
- Admit a forwarded command without the bounds of a local one
  (`admitForward`, review M2).
