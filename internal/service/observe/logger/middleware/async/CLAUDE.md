<!-- updated: 2026-09-28T16:42:12Z -->
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
| `decl_gen.go` | written by kit gen from the design (ADR 0170): the declarations of `DropPolicy` and `Config` — each struct with every field, unexported ones included. Their methods, constructors and helpers stay hand-written, in the files this document names |
| `async_sink.go`        | `asyncSink` + `New` + `Write` / `Flush` / `Close`; the production drainer runs on a `kernel/concur/worker.LoopDaemon`, whose idempotent `Stop` is the join (ADR 0014 §D6) |
| `async_sink_policy.go` | `DropPolicy` enum (`DropNewest` default, `DropOldest`) |
| `drainer.go`           | drainer body: `drain` (the loop body the daemon runs and the white-box tests spawn directly; closes `done` via `doneOnce`) / `drainLoop` (selects on the sink's `stop`) / `forward` / `drainRemaining`; `maxSaneCap` (64 KiB) bounds pool retention against attacker-influenced records |
| `runtime.go`           | helpers — `yieldOnce`, `isClosed`, `asyncCtx`, `forwardDownstreamError`, `swallowRingError` |
| `entry.go`             | `recordEntry` recycled through `recycler.Pool` |
| `internal/core/observe/logger/middleware/async` | its sentinels — range 0.3.17.\* — declared in the core mirror since ADR 0160; this package declares none |

## Behaviour

- **Default buffer.** `Config.BufferSize <= 0` → 1024-slot ring.
- **DropNewest** (default): full ring → drop the new entry, fire `OnDrop`,
  return `BufferFull`.
- **DropOldest**: evict the head, fire `OnDrop` for it, retry the write,
  return `nil`.
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
| 0.3.17.3  | `CtxCancelled` | `Write` / `Flush` saw a cancelled `ctx` |

## Do NOT

- Treat `Stopped` as a cancellation — it means the sink lifecycle ended.
- Share a `Config` between sinks expecting independent metric counters.
- Block in `OnDrop` / `OnError`: `OnDrop` runs inside `Write` with `ringMu`
  held, `OnError` on the drainer's hot path.

## Verification

```
bazel test --config=race //internal/service/observe/logger/middleware/async:async_test
```
