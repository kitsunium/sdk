# internal/kernel/worker/

## Purpose

The SDK's generic goroutine-lifecycle primitive. Stdlib-only, domain-neutral.
`LoopDaemon` spawns a single background goroutine running a `Loop`, exposes an
**idempotent** `Stop` that signals the loop to exit and **joins** it, and a
`Done` channel closed once the loop has returned. `Every` layers a ticker loop
on top. Collapses the byte-identical `stop/stopOnce/done/doneOnce` scaffold that
three real consumers each hand-rolled (ADR 0014 §D6).

## Surface

| Symbol | Signature | Use case |
|---|---|---|
| `Loop` | `func(stop <-chan struct{})` | the goroutine body; MUST return on `stop` |
| `LoopDaemon` | concrete struct | a running goroutine + idempotent stop + join |
| `Start` | `Start(loop Loop) *LoopDaemon` | spawn `loop`; close `Done` when it returns |
| `NewLoopDaemon` | `NewLoopDaemon(loop Loop) *LoopDaemon` | `New`-prefixed alias of `Start` (lint) |
| `LoopDaemon.Stop` | `Stop()` | idempotent: `close(stop)` once, then `<-done` |
| `LoopDaemon.Done` | `Done() <-chan struct{}` | closed when the loop has returned |
| `Every` | `Every(interval time.Duration, tick func()) *LoopDaemon` | ticker loop; `Stop` joins |

> Naming note: ADR 0014 §D6 names the type `Daemon`, but `KTN-STRUCT-ROLE`
> requires a recognized role *suffix* (a bare role noun is rejected), so the
> shipped type is `LoopDaemon` — `Daemon` is the role, `Loop` the qualifier.
> `Start` / `Every` are the idiomatic constructors; `NewLoopDaemon` is the
> `New`-prefixed alias the struct-constructor lint expects.

## Contract

- **The Loop MUST return promptly on `stop`.** A `Loop` that ignores its `stop`
  channel deadlocks `Stop`, which blocks on the join. Loops `select` on `stop`
  (see the async drainer) and exit their work loop when it is closed.
- **`Stop` is idempotent and joins.** A `sync.Once` guards `close(stop)`, so
  concurrent / repeated `Stop` never double-closes; every caller blocks on the
  closed `done` channel, so `Stop` returning means the loop has returned.
- **The Loop MUST NOT panic.** It runs in a bare goroutine — a panic crashes the
  process. `worker` adds NO `recover()` by deliberate choice (keep it minimal);
  callers running risky work recover inside their own `Loop`.
- **`Every` owns its `time.Ticker`** and stops it when the loop exits, so `Stop`
  both ends the ticking and joins the goroutine.

## Consumers it collapses

`worker.LoopDaemon` replaces the hand-rolled `stop/stopOnce/done/doneOnce`
lifecycle in:

- `internal/service/logger/middleware/async` — the drainer goroutine (retrofitted
  in the same commit that introduced this package, as the proving consumer).
- the s3 / cloudwatch batching sinks under `third-party/*` (cut over in a later
  commit of the same wave).

## Conventions

- **Concrete struct, no interface.** Like `recycler` (ADR 0010) and `snapshot`
  (ADR 0011), `worker` ships a concrete struct by deliberate choice — a
  single-impl interface would be over-abstraction. The `Daemon` role suffix on
  `LoopDaemon` passes `KTN-STRUCT-ROLE`; no IFACE-PLUGIN marker is needed.
- **One exported struct per file.** `LoopDaemon` lives in `worker.go`; `Every` is
  a func and shares `every.go` (KTN-STRUCT-ONEFILE governs structs, not funcs).
- **Emits NO codes.** Pure goroutine control, like `recycler` / `snapshot`. No
  `codes.go` / `errors.go`, no `audit_srcs` filegroup.

## Do NOT

- Write a `Loop` that ignores `stop` — it deadlocks `Stop`.
- Add a `recover()` here without a test that proves it is needed — the contract
  is "the Loop must not panic", documented above.
- Add worker pools, queues, supervision trees, or restart policy here — this is
  a single-goroutine lifecycle primitive, not a framework.

## Verification

```sh
bazel test --config=race //internal/kernel/worker:worker_test
```
