<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/logger/

## Purpose

Logger v2 — zero-allocation, multi-sink, decorator-friendly. Realises the
`core/logger.Handler` + `core/logger.Logger` ports declared in
`internal/core/logger`. Consumers import `pkg/v1/logger` rather than this
package; the public facade stamps `framework_version` onto every record and
hides the wiring.

The shape at a glance:

```
Logger ── Handler (genericHandler / TextHandler)
                  │
                  ├── Encoder (encoder/text)
                  └── Sink chain (middleware/* → sink/*)
```

- `loggerImpl` is the default `core.Logger`, a thin wrapper over a Handler.
- `genericHandler` (handler.go) composes an Encoder + a Sink — the v2 default.
- `TextHandler` (text_handler.go) is the legacy fused format+transport
  handler kept for backward compatibility; it still mutexes its writer and
  ships the same RFC3339-ms output as `encoder/text`.
- The `Builder` (builder.go) is the chainable, recycler-backed fluent API
  returned by `Build(lg, lv)`. Per-call cost in steady state: zero heap
  allocations once `recordPool` is warm.

## Contents

| File              | Role |
|---|---|
| `logger.go`       | `loggerImpl` + `New` + `Build`/`LogAttrs` package entries |
| `builder.go`      | `Builder` interface + `chainBuilder` impl (recycled via `recordPool`) |
| `handler.go`      | `genericHandler` (Encoder × Sink composition) + `NewHandler` |
| `text_handler.go` | `TextHandler` legacy fused handler (`NewTextHandler`) |
| `pool.go`         | `recordPool` — `buffer.Recycler[*chainBuilder]` with pre-sized attrs |
| `codes.go`        | `Code*` constants — ADR 0005 range **0.3.1.\*** |
| `errors.go`       | Sentinels — `WriterNil`, `HandlerNil`, `EncoderNil`, `SinkRequired`, `CtxCancelled`, `WriteFailed` |

## Conventions

- **One exported struct per file** (`KTN-STRUCT-ONEFILE`).
- **KTN-FUNC-MAXLOC ≤ 50.** `TextHandler.Handle` is intentionally split
  into `renderLine` + `writeLine` for that reason.
- **IFACE-PLUGIN.** `Build` returns the `Builder` *interface*, never the
  recycled concrete type — see ktn-linter `KTN-IFACE-PLUGIN`.
- **Origin wins on wrap.** `WriteFailed` / `CtxCancelled` wrap their cause
  via `errs.Wrap`; `errors.Is(err, context.Canceled)` still holds.
- **Caller PC capture.** Both `Log` and `Builder.Send` call
  `runtime.Callers(callerSkipDepth, …)` so handlers that resolve frames
  lazily see the application caller, not the logger plumbing.
- **Swallow on emit.** `Logger.Log` MUST NOT propagate handler errors —
  see `swallowHandlerError` in `logger.go`. A future commit may swap this
  for a configurable `OnError` hook.

## Error catalogue — range 0.3.1.\*

| Code      | Sentinel       | Trigger |
|---|---|---|
| 0.3.1.1   | `WriterNil`    | `NewTextHandler(nil, …)` |
| 0.3.1.2   | `HandlerNil`   | `New(nil)` |
| 0.3.1.3   | `EncoderNil`   | `NewHandler(nil, sink, …)` |
| 0.3.1.4   | `SinkRequired` | `NewHandler(enc, nil, …)` |
| 0.3.1.10  | `CtxCancelled` | `Handle(ctx, …)` with a cancelled `ctx` (wraps `context.Canceled`) |
| 0.3.1.20  | `WriteFailed`  | `io.Writer.Write` returned an error (EX_IOERR / exit 74) |

## Do NOT

- Call `Handle` directly from application code — go through a `Logger` so
  `Enabled` can short-circuit and so caller-PC capture sees the right frame.
- Reuse a `RecordEvent` after handing it to `Handle` — the handler may set
  `Time` in place.
- Use a `Builder` after `Send` — it has been returned to the recycler.
- Re-export anything from this package at `pkg/v1/*` directly. The public
  facade owns its own constructors.

## Verification

```
bazel test --config=race //internal/service/logger:logger_test
bazel test --config=race //internal/service/logger/...
```

## Subtree

- `encoder/` — `core/logger.Encoder` adapters (text today; ndjson/json later)
- `middleware/` — chainable `Sink` decorators (multi, async, route, failover, sample, recover)
- `sink/` — terminal `Sink` implementations (console, file, syslog)
