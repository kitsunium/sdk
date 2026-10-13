<!-- updated: 2026-10-13T12:00:00Z -->
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

## Do NOT

- Iterate a map where the order reaches a message: a run must stay a pure
  function of its seed.
