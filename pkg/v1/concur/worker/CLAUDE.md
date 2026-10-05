<!-- updated: 2026-10-05T12:00:00Z -->
# pkg/v1/concur/worker/

## Purpose

Public facade for `internal/kernel/concur/worker` (ADR 0159 §4): one
background goroutine with an idempotent `Stop` that signals and joins it, and
`Every`, the same daemon ticking on an injected clock. Aliases and forwarding
functions only — no behaviour of its own. Stdlib-only → dep-light; cross-OS
portable.

It is the loop the SDK's network server, SSE and WebSocket heartbeats, async
logger drainer and rotating file writer run on, published so a program — and
the framework, which reaches the SDK through `pkg/v1` alone (ADR 0147) — stops
writing the stop/stopOnce/done/doneOnce scaffold ADR 0014 §D6 collapsed.

## Surface

| Symbol | Notes |
|---|---|
| `Loop` | alias of `func(stop <-chan struct{})`; MUST return once `stop` closes, MUST NOT panic |
| `LoopDaemon` | alias; `Stop()` (idempotent, joins) and `Done()`. Behind the pointer a constructor returns |
| `Start(loop)` | spawns the loop; a nil loop panics here |
| `NewLoopDaemon(loop)` | `Start` under the New-prefixed name the kernel's struct-constructor lint asks for |
| `Every(interval, tick, opts…)` | a ticking daemon; a nil tick or a non-positive interval panics here; a tick due while one runs is dropped |
| `EveryOption` | alias; built by `WithClock` / `WithDone` only (its parameter type is unexported) |
| `WithClock(w)` | tick on a `clock.Waiter` — `pkg/v1/clock`'s, named in the signature so the README shows an importable type |
| `WithDone(done)` | end the loop when the owner's work ends on its own; `Stop` still joins |

That is the complete exported surface of `internal/kernel/concur/worker`,
`NewLoopDaemon` included: a pure alias facade holds nothing back.

## Conventions

- **The one sibling import is `pkg/v1/clock`**, for `WithClock`'s parameter —
  the type is the kernel's `clock.Waiter` either way, and naming the public
  alias keeps an internal package out of the consumer's documentation, as
  `data/docstore` names `data/sql`'s aliases.
- **A ManualClock is waited for before it is moved.** `Every` arms its ticker
  on the daemon's own goroutine, so a test calls `BlockUntil(1)` before
  `Advance`; the package comment says so, and both tests do it.
- **Forwarding functions, not `var` aliases** (see `data/semver`); methods are
  documented on the alias and linked `[LoopDaemon].Stop` (ADR 0138).
- The package comment is in `doc.go`, which kit writes from the design
  (`design/kernel/concur.yaml`, ADR 0167): edit the design, run `kit gen`, then `make api`
  and `make docs-readme`. `README.md` is written by `tools/genindex` from
  `docs/api`; do not hand-edit it.

## Do NOT

- Add a recovering loop, a restart policy or an error channel here. A loop
  that must keep running is `app/lifecycle`'s supervisor (ADR 0112); a shape
  the alias cannot express lands in the kernel package first.
- Reach for `internal/kernel/concur/worker` or `internal/kernel/clock` in this
  package's tests.

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/kernel/concur.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: `doc.go` holds the package comment, which kit writes from the design (ADR 0167), and the hand-written files the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```
bazel test --config=race //pkg/v1/concur/worker:worker_test
cd pkg && GOWORK=off go test -race ./v1/concur/worker/
```

`worker_external_test.go` is `package worker_test` and names `pkg/v1` only:
`Stop` returns only after the loop has, through `Start` and `NewLoopDaemon`;
`Every` ticks once per interval of a `pkg/v1/clock.ManualClock` and leaves
nothing armed after `Stop`; `WithDone` ends the loop without `Stop`; the three
mistakes panic at the call — plus `ExampleEvery`.
