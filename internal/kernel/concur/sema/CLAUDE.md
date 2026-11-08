<!-- updated: 2026-11-03T22:00:00Z -->
# internal/kernel/concur/sema/

## Purpose

Generic **weighted semaphore** (#812): `Weighted` holds a capacity of units;
`Acquire(ctx, n)` takes `n` of them, FIFO and cancellable; `Release(n)` gives
them back. Where the SDK's admission controls (`group`'s limit, the bulkhead,
the waiting bulkhead) count calls, this counts their cost. A kernel primitive
(stdlib-only AND generic). Code range `0.1.7.*` (`WEIGHT_TOO_LARGE`). Decided
by **ADR 0216**. Not published (like `commit`, ADR 0207) until a domain adopts
it.

## Contents

| File | Surface |
|---|---|
| `sema.go` | `Weighted` + `NewWeighted` / `Acquire` / `TryAcquire` / `Release`, the waiter queue |
| `gen_codes.go` | `CodeWeightTooLarge` (`0.1.7.1`) and `WeightTooLarge`, written by kit gen from `design/kernel/concur.yaml` |
| `perf_fixtures_test.go` | race off: the fixture of the design's budget on `(*Weighted).Acquire` (0 allocations uncontended, ADR 0165) |

## Conventions

- **FIFO.** `Acquire` takes the units at once only when nobody waits; a
  waiter is served only when every earlier one has been — `grant` stops at the
  first waiter that does not fit, so a large request is never starved by the
  small ones behind it. `TryAcquire` refuses rather than overtake a waiter.
- **Refused at once, never parked forever** (ADR 0031): `n` above the capacity
  returns `WeightTooLarge` before any wait; a capacity below 1 is clamped to 1.
- **Cancellation holds nothing.** A waiter whose context ends is unlinked; if
  its units were granted in the same instant it gives them back. Either way
  the waiters behind it are served again, since it may have been the one
  blocking them. A context already done fails even when the units are free.
- **Misuse panics**: a negative weight, and a `Release` of more than is held
  — the recycler's position on a programmer's fault; a silent correction
  would leave an admission bound that no longer bounds.
- **Errors**: `WeightTooLarge`, and the context's own error, unwrapped, as
  `commit` returns it.

## Adoption

None yet. The candidates, siblings of their own domains (ADR 0039): a
weighted bulkhead in `app/resilience` (`WeightOf(ctx) int64`), `forward`'s
`MaxBytes` accounting, `net/server`'s bytes of read buffers in flight.

## Do NOT

- Let a small request overtake a waiting large one.
- Park a request heavier than the capacity.

## Verification

```
cd internal && go test -race -count=5 ./kernel/concur/sema/
cd internal && go test -run '^$' -bench . -benchmem -cpu 1,8 ./kernel/concur/sema/
```
