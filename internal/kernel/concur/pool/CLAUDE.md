<!-- updated: 2026-11-03T22:00:00Z -->
# internal/kernel/concur/pool/

## Purpose

Generic **bounded worker pool** (#804): `Pool` runs `Task`s on a fixed set of
long-lived workers fed by a bounded queue, refuses when there is no room,
drains, and keeps a task's panic on its worker. A kernel primitive
(stdlib-only AND generic — no domain word). Code range `0.1.6.*` (`POOL_FULL`,
`POOL_CLOSED`). Decided by **ADR 0216**.

**Published as `pkg/v1/concur/pool`** (ADR 0159 §4, ADR 0216): a pure alias,
so every exported shape here is public API — a renamed field, a changed
signature or a new method breaks a consumer at compile time, allowed only
while the module is v0 and said out loud (ADR 0040).

It is the shape `group` and `worker` are not: `group` is a one-shot fan-out
(a goroutine per task, one wait, used once, a panic RE-RAISED in the waiter
because there is one); `worker` is one daemon. A pool outlives every task, so
a task's panic is handed to `Config.OnPanic` and the worker serves on — and
with no `OnPanic` it is raised again, never swallowed.

## Contents

| File | Surface |
|---|---|
| `pool.go` | `Pool` + `NewPool` / `Submit` / `Drain` / `Stop`, `Task`, `PanicValue`, the workers |
| `config.go` | `Config` — `Workers` / `Queue` / `OnPanic` / `OnError` |
| `gen_codes.go` | `CodePoolFull` (`0.1.6.1`), `CodePoolClosed` (`0.1.6.2`) and their sentinels, written by kit gen from `design/kernel/concur.yaml` |
| `perf_fixtures_test.go` | race off: the fixture of the design's budget on `(*Pool).Submit` (0 allocations, ADR 0165) |

## How it works

- **The queue is a channel of `Task`**: unbuffered when `Queue` is 0, so a
  non-blocking send lands only on a worker parked in its receive — "an idle
  worker" — and fails otherwise: `PoolFull` at once, never a park (ADR 0031).
  Buffered when `Queue` > 0: a full one is waited on within the submitter's
  context, then `PoolFull` wraps its error.
- **Closing never meets a send.** `Submit` sends under the read lock with
  `closed` false; closing closes `quit` first (waking a `Submit` waiting for
  room), then takes the write lock, sets `closed` and closes the channel. A
  task accepted is therefore always in the channel the workers range over.
- **`Drain`** closes and waits for the workers within its context — they run
  every queued task, then exit. **`Stop`** sets `abandon` (a worker drops each
  task it takes), cancels the tasks' context, closes, and waits within its
  context. Neither join is unbounded (ADR 0072); a task that ignores its
  context outlives them, and the doc says so.
- **A task runs under the pool's context** (New's, cancelled by `Stop`), not
  a merge with the submitter's values: that merge is a context per task, an
  allocation per `Submit`, and the budget is 0 (ADR 0216 §2).

## Adoption

None in the tree yet, which is stated rather than dressed up (ADR 0025
admitted `cache` the same way). The candidates, each a change of its own
domain: health's readiness probe (a goroutine and a context per check per
probe today, `pkg/v1/app/health` BENCH), the scheduler's fires, `forward`'s
senders. `queue`'s `Consume` keeps its pull loops — not a fit.

## Do NOT

- Park a `Submit` on a pool with no queue. The refusal is the contract.
- Swallow a panic when `OnPanic` is nil.
- Give `Stop` or `Drain` an unbounded join.

## Verification

```
cd internal && go test -race -count=5 ./kernel/concur/pool/
cd internal && go test -run '^$' -bench . -benchmem ./kernel/concur/pool/
```
