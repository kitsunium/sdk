<!-- updated: 2026-11-03T12:00:00Z -->
# internal/service/observe/logger/middleware/async/

## Purpose

Non-blocking `Sink` decorator: a lock-light SPSC ring buffer plus a single
drainer goroutine. Producers call `Write` synchronously but never block on
downstream I/O — entries land in the ring and the drainer sips them out
into the wrapped sink.

Use case: shield the hot path from slow remote sinks (CloudWatch, HTTP,
S3) so a backed-up drain never stalls the application.

## Contents

| File | Role |
|---|---|
| `async_sink.go`        | `asyncSink` + `New` + `Write` / `Flush` / `Close`; the `Block` wait (`blockForSpace` / `retryBlocked` / `signalSpace` / `abandon`); the production drainer runs on a `kernel/concur/worker.LoopDaemon`, whose idempotent `Stop` is the join (ADR 0014 §D6) |
| `async_sink_policy.go` | `DropPolicy` enum (`DropNewest` default, `DropOldest`, `Block`) + `DefaultMaxBlock` (1 s) |
| `drainer.go`           | drainer body: `drain` (the loop body the daemon runs and the white-box tests spawn directly; closes `done` via `doneOnce`) / `drainLoop` / `park` + `pending` (the idle wait on `wake`, #806) / `take` (up to `batchMax` entries per lock) / `deliver` → `forward` (one `Write`) or `forwardBatch` (one `WriteBatch`, #807) / `settle` / `recycle` / `drainRemaining`; `maxSaneCap` (64 KiB) bounds pool retention against attacker-influenced records |
| `runtime.go`           | helpers — `isClosed`, `asyncCtx`, `forwardDownstreamError`, `swallowRingError` |
| `async_sink_entry.go`  | `recordEntry` recycled through `recycler.Pool` |
| `config.go`            | `Config{BufferSize, Policy, MaxBlock, Clock, OnDrop, BatchMax, OnError}` |
| `internal/core/observe/logger/middleware/async` | its sentinels — range 0.3.17.\* — declared in the core mirror since ADR 0160; this package declares none |

## Behaviour

- **An idle drainer parks (#806).** On an empty ring the drainer sets
  `parked`, looks at the ring once more under `ringMu` (the Dekker step: a
  Write that landed before the flag was visible is found here), then waits on
  `wake` or `stop`. A producer that accepted an entry reads `parked` AFTER its
  `TryWrite` and, when it is set, leaves a token on the one-slot `wake` — so
  one of the two always sees the other and no wake-up is lost. A busy drainer
  costs a producer one atomic load. The old loop yielded with `Gosched`, which
  with nothing else runnable returned at once: one P at 100 % for a process
  that logged nothing (`TestAnIdleAsyncSinkCostsNoCPU`, Linux: 690 ms of CPU
  over an idle 500 ms on the base, under 25 ms now).
- **Batches (#807).** `Config.BatchMax > 1` and a downstream that implements
  `corelogger.BatchSink` (file, rotfile, the level gate, the tcp writer): the
  drainer takes up to `BatchMax` entries per `ringMu` acquisition and hands them
  over in one `WriteBatch` — one `write(2)` for a file. A downstream without the
  port, or `BatchMax <= 1` (the zero Config), gets one `Write` per record. A
  failed `WriteBatch` fires `OnError` once per record it did not deliver
  (`len - written`), never once per batch. The scratch slices (`batch`, `recs`,
  `payloads`) belong to the single drainer goroutine and are cleared after
  every call, so no record outlives its delivery through them.
- **Default buffer.** `Config.BufferSize <= 0` → 1024-slot ring.
- **DropNewest** (default): full ring → drop the new entry, fire `OnDrop`,
  return `BufferFull`.
- **DropOldest**: evict the head, fire `OnDrop` for it, retry the write,
  return `nil`.
- **Block** (#479): the Write waits for a free slot instead of losing the
  entry — Vector's `when_full: block` — OUTSIDE `ringMu`, so the drainer can
  free the slot and `Close` can reach the waiter. The wait is bounded by
  `MaxBlock` on `Clock` (ADR 0090); a zero `MaxBlock` is `DefaultMaxBlock`,
  one second, never "forever" (ADR 0031) — a logger blocking without a bound
  turns a dead downstream into a stalled service. Past the bound the entry is
  dropped, `OnDrop` fires and Write returns `BlockTimeout`; a cancelled context
  returns `CtxCancelled` (and fires `OnDrop`), `Close` returns `Stopped` to
  every waiter. The drainer signals a one-slot `space` channel after every
  read; a waiter that lands and leaves room passes the signal on. The timer is
  armed only on the saturated path, so a Write that finds room never touches
  the clock; `space` is nil under the drop policies.
- **Cancellation.** `Write` with a cancelled `ctx` returns `CtxCancelled`
  wrapping `ctx.Err()`; `Flush` does too while entries remain (a drained
  ring goes straight to the downstream `Flush`). A nil ctx is allowed and
  means "wait forever" — `Flush` waits on `flushSignal` rather than spinning.
- **Stopped vs cancelled.** `Flush` returns `Stopped` (not a cancellation)
  when it observes `flushSignal` closed. Nothing in production closes that
  channel — only the white-box tests do — so a drainer exit does not produce
  it. Callers distinguish the two via `errs.HasCode` / `errors.Is`.
- **ringMu.** A single mutex around every ring access serialises the
  effective producers (`Write`, `Close`, `DropOldest`'s read-then-write
  retry) against the drainer's read. Closes the Close-vs-Write TOCTOU race.
- **Lifecycle via worker.LoopDaemon.** The production drainer goroutine is
  owned by a `kernel/concur/worker.LoopDaemon`; its idempotent `Stop` joins the loop.
  `Close` closes the `stop` channel under `ringMu` (guarded by `stopOnce` so
  repeated Close never double-closes) then joins the daemon, preserving the
  `drainRemaining` "lose nothing accepted" contract. The white-box test path
  builds the sink without a daemon and joins on the `done` channel that
  `drain()` closes via `doneOnce` (ADR 0014 §D6).
- **Pool amplification guard.** After `forward()`, entries with
  `cap(data) > maxSaneCap` drop the slice so the pool never retains
  pathologically large buffers (CWE-400 / CWE-789).

## Error catalogue — range 0.3.17.\*

Declared in `internal/core/observe/logger/middleware/async` since ADR 0160 §2: this engine returns the sentinels below and declares none, so a test or a caller names them `coreasync.X`.

| Code      | Sentinel       | Trigger |
|---|---|---|
| 0.3.17.1  | `Stopped`      | `Write` after `Close` (drainer is gone) |
| 0.3.17.2  | `BufferFull`   | ring saturated under `DropNewest` |
| 0.3.17.3  | `CtxCancelled` | `Write` / `Flush` saw a cancelled `ctx`, or a `Block` wait was cancelled |
| 0.3.17.4  | `BlockTimeout` | the ring stayed full for `MaxBlock` under `Block` |

## Do NOT

- Treat `Stopped` as a cancellation — it means the sink lifecycle ended.
- Share a `Config` between sinks expecting independent metric counters.
- Block in `OnDrop` / `OnError`: `OnDrop` runs inside `Write` with `ringMu`
  held, `OnError` on the drainer's hot path.
- Read `parked` before the `TryWrite` in a producer, or set it after the
  drainer's second look: either order reopens the lost-wakeup window.
- Go back to a `Gosched` (or any polling) idle loop.

## Verification

```
bazel test --config=race //internal/service/observe/logger/middleware/async:async_test
```
