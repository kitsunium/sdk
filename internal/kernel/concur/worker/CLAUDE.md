<!-- updated: 2026-10-03T11:00:00Z -->
# internal/kernel/concur/worker/

## Purpose

The SDK's generic goroutine-lifecycle primitive. Stdlib-only, domain-neutral.
`LoopDaemon` spawns a single background goroutine running a `Loop`, exposes an
**idempotent** `Stop` that signals the loop to exit and **joins** it, and a
`Done` channel closed once the loop has returned. `Every` layers a ticker loop
on top. Collapses the byte-identical `stop/stopOnce/done/doneOnce` scaffold that
three real consumers each hand-rolled (ADR 0014 §D6).

**Published as `pkg/v1/concur/worker`** (ADR 0159 §4): a pure alias, so every exported shape here is public API — a renamed field, a changed signature or a new method on an exported interface breaks a consumer at compile time, allowed only while the module is v0 and said out loud (ADR 0040).

## Surface

| Symbol | Signature | Use case |
|---|---|---|
| `Loop` | `func(stop <-chan struct{})` | the goroutine body; MUST return on `stop` |
| `LoopDaemon` | concrete struct | a running goroutine + idempotent stop + join |
| `Start` | `Start(loop Loop) *LoopDaemon` | spawn `loop`; close `Done` when it returns |
| `NewLoopDaemon` | `NewLoopDaemon(loop Loop) *LoopDaemon` | `New`-prefixed alias of `Start` (lint) |
| `LoopDaemon.Stop` | `Stop()` | idempotent: `close(stop)` once, then `<-done` |
| `LoopDaemon.Done` | `Done() <-chan struct{}` | closed when the loop has returned |
| `Every` | `Every(interval time.Duration, tick func(), opts ...EveryOption) *LoopDaemon` | ticker loop; `Stop` joins |
| `EveryOption` | `func(everyConfig) everyConfig` | tunes `Every`; applied in order, a nil one skipped; by value, so the option set never escapes to the heap |
| `WithClock` | `WithClock(w clock.Waiter) EveryOption` | tick on `w` instead of the wall clock — a `ManualClock` in a test; nil is the wall clock |
| `WithDone` | `WithDone(done <-chan struct{}) EveryOption` | also end the loop when `done` closes — the owner's own end, without waiting for `Stop` |

> Naming note: ADR 0014 §D6 names the type `Daemon`, but `KTN-STRUCT-ROLE`
> requires a recognized role *suffix* (a bare role noun is rejected), so the
> shipped type is `LoopDaemon` — `Daemon` is the role, `Loop` the qualifier.
> `Start` / `Every` are the idiomatic constructors; `NewLoopDaemon` is the
> `New`-prefixed alias the struct-constructor lint expects.

## Contract

- **The Loop MUST return promptly on `stop`.** A `Loop` that ignores its `stop`
  channel deadlocks `Stop`, which blocks on the join. Loops `select` on `stop`
  (see the `net/sse` and `net/websocket` watchers) and exit their
  work loop when it is closed. A loop that ends by other means must be ended
  before `Stop`, or joined through `Done`: the async drainer exits on its
  sink's own stop channel, which `Close` closes before it calls `Stop`, and
  `net/server`'s `Serve` loop returns when its listener closes, the adapter
  waiting on `Done` rather than calling `Stop`.
- **A nil `Loop`, a nil `tick` or a non-positive `Every` interval panics** at
  the call, not inside the spawned goroutine.
- **`Stop` is idempotent and joins.** A `sync.Once` guards `close(stop)`, so
  concurrent / repeated `Stop` never double-closes; every caller blocks on the
  closed `done` channel, so `Stop` returning means the loop has returned.
- **The Loop MUST NOT panic.** It runs in a bare goroutine — a panic crashes the
  process. `worker` adds NO `recover()` by deliberate choice (keep it minimal);
  callers running risky work recover inside their own `Loop`.
- **`Every` owns its ticker** and stops it when the loop exits, so `Stop` both
  ends the ticking and joins the goroutine. The ticker is a `clock.Ticker` built
  on `clock.System` unless `WithClock` names another clock, so a consumer's
  cadence is testable by advancing a `ManualClock` instead of sleeping; the
  loop arms it on its own goroutine, so a test calls `BlockUntil` before its
  first `Advance`. A tick that comes due while the previous one still runs is
  dropped, never queued — the `clock.Ticker` contract, which is `time.Ticker`'s.
- **`WithDone` is an early end, not a replacement for `Stop`.** An owner whose
  work can finish on its own — a stream the peer closed, a connection a tick
  found dead — hands its end channel over, so the ticking stops at that end;
  `Stop` remains the join and returns at once on a loop that already left.

## Consumers it collapses

`worker.LoopDaemon` owns the background goroutine, in place of a hand-rolled
`stop/stopOnce/done/doneOnce` lifecycle, in:

- `internal/service/observe/logger/middleware/async` — the drainer goroutine (retrofitted
  in the same commit that introduced this package, as the proving consumer).
- `internal/service/net/server` — the goroutine running `http.Server.Serve`.
- `internal/service/net/sse` and `internal/service/net/websocket` — each
  stream's or connection's drain watcher (`Start`) and its keep-alive /
  heartbeat (`Every`, on the stream's or connection's clock, ended early by
  `WithDone` on the stream's own end).
- `internal/service/observe/logger/writer/rotfile` — interval rotation, through `Every`.

The s3 / cloudwatch batching sinks under `third-party/aws/writer/*` do not use
it: they are built on `kernel/concur/batcher`, which drives its own `time.Ticker`
(ADR 0014 §D6).

## Conventions

- **Concrete struct, no interface.** Like `recycler` (ADR 0010) and `snapshot`
  (ADR 0011), `worker` ships a concrete struct by deliberate choice — a
  single-impl interface would be over-abstraction. The `Daemon` role suffix on
  `LoopDaemon` passes `KTN-STRUCT-ROLE`; no IFACE-PLUGIN marker is needed.
- **Types that belong to one another share a file** (`KTN-STRUCT-PARTITION`).
  `LoopDaemon` lives in `worker.go`; `Every` is a func and shares `every.go`.
- **Emits NO codes.** Pure goroutine control, like `recycler` / `snapshot`. No
  `codes.go` / `errors.go`, no `audit_srcs` filegroup.
- **Imports only `kernel/clock`** besides the stdlib — the time port the ticker
  is built on.

## Do NOT

- Write a `Loop` that ignores `stop` — it deadlocks `Stop`.
- Add a `recover()` here without a test that proves it is needed — the contract
  is "the Loop must not panic", documented above.
- Add worker pools, queues, supervision trees, or restart policy here — this is
  a single-goroutine lifecycle primitive, not a framework.

## Verification

```sh
bazel test --config=race //internal/kernel/concur/worker:worker_test
```

## Declarations

`decl_gen.go` is written by kit gen from the design (ADR 0170): the declarations of `EveryOption`, `Loop` and `LoopDaemon` — each struct with every field, unexported ones included. Their methods, constructors and helpers stay hand-written, in the files this document names.
