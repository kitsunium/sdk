<!-- updated: 2026-10-03T12:00:00Z -->
# internal/core/observe/logger/sink/console/

## Purpose

The codes and sentinels of the logger's console sink, whose engine is
`internal/service/observe/logger/sink/console`. The range `0.3.13.*` was allocated to that engine
(ADR 0005) and is declared here since ADR 0160 §2, which puts every code of a
service package in the core package at the same path: the engine returns these
sentinels and declares none, and `pkg/v1` aliases them from here when it
publishes them (ADR 0074).

Nothing else lives here. It has no port of its own: the sink is a `Sink` of `internal/core/observe/logger`.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `CodeWriterNil`, `CodeCtxCancelled`, `CodeWriteFailed` | `errs.Code` constants | one per sentinel, literal values |
| `WriterNil`, `CtxCancelled`, `WriteFailed` | `*errs.Error` sentinels | built by `errs.Define`; match with `errs.HasCode` or `errors.Is` |

## Error codes — range 0.3.13.*

| Code | Constant | Sentinel | Reason | Exit / status |
|---|---|---|---|---|
| 0.3.13.1 | `CodeWriterNil` | `WriterNil` | `WRITER_NIL` | — |
| 0.3.13.10 | `CodeCtxCancelled` | `CtxCancelled` | `CTX_CANCELLED` | — |
| 0.3.13.20 | `CodeWriteFailed` | `WriteFailed` | `WRITE_FAILED` | 74 (EX_IOERR) |

## Do NOT

- Renumber a code to match this directory. `LL = 3` records the layer that
  ALLOCATED the range, not the one that declares it, and a consumer branches on
  the value (ADR 0160 §3); `codeRangeOwners` maps the unchanged key here.
- Declare a code back in the engine: a service package declares none
  (ADR 0160 §2).
- Add behaviour here. What the sink does is the engine's; this package
  carries only what a caller matches a failure against.

## Verification

```sh
cd internal/core && GOWORK=off go test -race -count=1 ./observe/logger/sink/console/
bazel test //internal/core/observe/logger/sink/console:console_test
bazel test //internal/kernel/errs:errs_test   # range ownership + uniqueness audits
```
