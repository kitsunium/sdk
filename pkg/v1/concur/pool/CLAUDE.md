<!-- updated: 2026-11-03T22:00:00Z -->
# pkg/v1/concur/pool/

## Purpose

Public facade for `internal/kernel/concur/pool` (ADR 0159 §4, ADR 0216): a
fixed set of long-lived workers over a bounded queue — `Submit` refused when
there is no room, `Drain` and `Stop` joined within their context, a task's
panic kept on its worker and handed to `Config.OnPanic`. Aliases, one
forwarding constructor and the two sentinels — no behaviour of its own.
Stdlib-only → dep-light; cross-OS portable.

Published for the program — and the framework — that would otherwise
hand-roll a request pipeline on a goroutine per task, usually without the
panic capture (#804).

## Surface

| Symbol | Notes |
|---|---|
| `Pool` | alias; `Submit` / `Drain` / `Stop`, each taking a context. Safe from any number of goroutines |
| `Config` | alias; `Workers` (at least 1), `Queue` (0: refuse at once), `OnPanic` (nil: raised again), `OnError` (nil: discarded). The zero value is usable |
| `Task` | alias of `func(ctx) error`; receives the pool's context, cancelled by `Stop` |
| `PanicValue` | alias; `Raised`, `Stack`, `String()` |
| `NewPool(ctx, cfg)` | starts the workers, joined by `Drain` or `Stop` |
| `PoolFull` | `0.1.6.1` — no room: every worker busy and no queue, or a full queue waited on until the submitter's context ended |
| `PoolClosed` | `0.1.6.2` — `Submit` after `Drain` or `Stop` |

That is the complete exported surface of `internal/kernel/concur/pool` except
the two `Code*` constants: a caller matches a sentinel with `errors.Is`, or by
code with `errs.HasCode` (ADR 0160 §3).

## Conventions

- The package comment is in `doc.go`, which kit writes from the design
  (`design/kernel/concur.yaml`, ADR 0167): edit the design, run `kit gen`,
  then `make api` and `make docs-readme`. `README.md` is written by
  `tools/genindex` from `docs/api`; do not hand-edit it.
- Methods and fields are documented on the alias and linked `[Pool].Submit`,
  `[Config].Queue` (ADR 0138).

## Do NOT

- Give the facade behaviour: a shape an alias cannot express lands in the
  kernel package first.
