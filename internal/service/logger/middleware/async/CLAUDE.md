<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/logger/middleware/async/

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
| `async_sink.go`        | `asyncSink` + `New` + `Write` / `Flush` / `Close`; production drainer lifecycle runs on a `kernel/worker.LoopDaemon` (replaces the former hand-rolled `stop/stopOnce/done/doneOnce` scaffold — ADR 0014 §D6) |
| `async_sink_policy.go` | `DropPolicy` enum (`DropNewest` default, `DropOldest`) |
| `drainer.go`           | drainer body: `drain` (test entry, closes `done`) / `drainLoop` (the `worker.Loop`, selects on `stop`) / `forward` / `drainRemaining`; `maxSaneCap` (64 KiB) bounds pool retention against attacker-influenced records |
| `runtime.go`           | helpers — `yieldOnce`, `isClosed`, `asyncCtx`, `forwardDownstreamError`, `swallowRingError` |
| `entry.go`             | `recordEntry` recycled through `recycler.Pool` |
| `config.go`            | `Config{BufferSize, Policy, OnDrop, OnError}` |
| `codes.go`, `errors.go`| sentinels — range 0.3.17.\* |

## Behaviour

- **Default buffer.** `Config.BufferSize <= 0` → 1024-slot ring.
- **DropNewest** (default): full ring → drop the new entry, fire `OnDrop`,
  return `BufferFull`.
- **DropOldest**: evict the head, fire `OnDrop` for it, retry the write,
  return `nil`.
- **Cancellation.** `Write` / `Flush` with a cancelled `ctx` return
  `CtxCancelled` wrapping `ctx.Err()`. A nil ctx is allowed and means
  "wait forever" — `Flush` waits on `flushSignal` rather than spinning.
- **Stopped vs cancelled.** `Flush` returns `Stopped` (not a cancellation)
  when `flushSignal` is observed closed — i.e. the drainer exited. Callers
  distinguish the two via `errs.HasCode` / `errors.Is`.
- **ringMu.** A single mutex around every ring access serialises the
  effective producers (`Write`, `Close`, `DropOldest`'s read-then-write
  retry) against the drainer's read. Closes the Close-vs-Write TOCTOU race.
- **Lifecycle via worker.LoopDaemon.** The production drainer goroutine is
  owned by a `kernel/worker.LoopDaemon`; its idempotent `Stop` joins the loop.
  `Close` closes the `stop` channel under `ringMu` (guarded by `stopOnce` so
  repeated Close never double-closes) then joins the daemon, preserving the
  `drainRemaining` "lose nothing accepted" contract. The white-box test path
  builds the sink without a daemon and joins on the `done` channel that
  `drain()` closes via `doneOnce` (ADR 0014 §D6).
- **Pool amplification guard.** After `forward()`, entries with
  `cap(data) > maxSaneCap` drop the slice so the pool never retains
  pathologically large buffers (CWE-400 / CWE-789).

## Error catalogue — range 0.3.17.\*

| Code      | Sentinel       | Trigger |
|---|---|---|
| 0.3.17.1  | `Stopped`      | `Write` after `Close` (drainer is gone) |
| 0.3.17.2  | `BufferFull`   | ring saturated under `DropNewest` |
| 0.3.17.3  | `CtxCancelled` | `Write` / `Flush` saw a cancelled `ctx` |

## Do NOT

- Treat `Stopped` as a cancellation — it means the sink lifecycle ended.
- Share a `Config` between sinks expecting independent metric counters.
- Block in `OnDrop` / `OnError`; the drainer calls them on its hot path.

## Verification

```
bazel test --config=race //internal/service/logger/middleware/async:async_test
```
