<!-- updated: 2026-09-11T00:00:00Z -->
# internal/service/logger/

## Purpose

Logger v2 — one allocation per emit, multi-sink, decorator-friendly. Realises the
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
  ships the same RFC3339-ms output as `encoder/text`. That last claim was
  aspirational until 2026-09-04: the file carried the value table **twice**
  (`appendAttr` and `appendValueOnly`) and both copies had drifted, rendering
  only String/Int64/Bool/Float64 and degrading Duration, Time and Uint64 to
  `?` where `encoder/text` rendered them. `appendAttr` now delegates to
  `appendValueOnly`, so one table remains and it matches the encoder.
  `KindAny` still degrades to `?` — that is the documented contract, pinned by
  `text_handler_internal_test.go`; `KindGroup` degrades too, since producers
  flatten groups into dotted keys rather than emitting a group payload.
  The same drift had left the **framing scrub** behind: `encoder/text` has
  replaced CR, LF and NUL with a space in the message, the attribute keys and
  the group names since V110, and this handler appended all three verbatim — so
  a `\n` in any of them forged a log line (CWE-117), and since ADR 0062 the real
  `trace_id`/`span_id` landed on the forged one. All four write sites (message,
  group name, key with and without a group) now call `encoder.AppendSanitized`,
  the encoder's own scrub, exported for this caller rather than copied — one
  function cannot drift from itself — and it costs nothing: `Default()`'s path
  stays at **0 mallocs per emit**. `TestTextHandler_FramesEveryByteAsTheEncoderDoes`
  sweeps all 256 byte values through each of the four positions and requires the
  handler's line to be byte-identical to the encoder's.
- The `Builder` (builder.go) is the chainable, recycler-backed fluent API
  returned by `Build(lg, lv)`. Per-call cost in steady state: **one** heap
  allocation per emit once `recordPool` is warm — the pool recycles the
  `*chainBuilder` and its attrs scratchpad, but the handler clones that
  scratchpad (`mergeAttrs` / `slices.Clone`) on every `Send`, so one slice
  escapes. Measured in `pkg/v1/logger/BENCH.md`; pinned by
  `TestV116BuildSendAllocatesOnePerEmit` (which runs only in the race-off
  alloc lane — see root `CLAUDE.md` rule 12).

## Contents

| File              | Role |
|---|---|
| `logger.go`       | `loggerImpl` + `New` / `NewWithTraceContext` + `Build`/`LogAttrs` package entries |
| `builder.go`      | `Builder` interface + `chainBuilder` impl (recycled via `recordPool`) |
| `handler.go`      | `genericHandler` (Encoder × Sink composition) + `NewHandler` |
| `text_handler.go` | `TextHandler` legacy fused handler (`NewTextHandler`) |
| `pool.go`         | `recordPool` — `recycler.Pool[*chainBuilder]` with pre-sized attrs |
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
- **Trace correlation is injected, never imported** (ADR 0062).
  `NewWithTraceContext(h, src)` binds a `core/logger.TraceContextSource`; the
  three emission paths (`Log`, `LogAttrs`, `Builder.Send`) call it and stamp
  `RecordEvent.TraceContext`. This package therefore keeps **zero edges to any
  other service-layer domain** — it never learns that `trace` exists, which is
  the point, since `internal/service/trace` is a sibling it may not import.
  `pkg/v1/logger` supplies the binding; `New(h)` leaves it nil and a nil source
  costs nothing per emit.
- **The source is read AFTER the `Enabled` gate**, in all three paths, so a
  record dropped by the level threshold pays no `context` walk. It is read
  before `Handle`, so every Handler, Sink and middleware sees the identity on
  the record.
- **A `!race` alloc guard covers this.** `TestT34TraceCorrelationAddsNoAllocation`
  in `pkg/v1/logger` asserts **exactly 1** alloc/op on all three emission paths,
  in and out of a span — stricter than `TestV116BuildSendAllocatesOnePerEmit`,
  which only asserts `>= 1`. Same race-off alloc lane; profiled in
  `pkg/v1/logger/BENCH.md`.

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
- Restamp a non-zero `RecordEvent.Time` from a sink or encoder. **Time is
  owned by the handler layer (V25):** `genericHandler.Handle` (and the legacy
  `TextHandler`) carry an injected `clock.Clock` and stamp `Time` exactly once
  at the handler boundary when it is zero, so the encoded line and every
  downstream sink observe one coherent instant. An encoder/sink may fill a
  zero `Time` only as a backward-compatible fallback; it MUST NOT overwrite a
  non-zero one.
- Use a `Builder` after `Send` — it has been returned to the recycler.
- Write a message, attribute key or group name into a `TextHandler` line
  without `encoder.AppendSanitized`, or give the handler its own scrub. The
  defect this rule records was a missing call, not a wrong scrubber.
- Re-export anything from this package at `pkg/v1/*` directly. The public
  facade owns its own constructors.

## The one-allocation claim, measured through a real sink chain

`BENCH.md` in this directory carries the cross-cutting report for the whole
subtree — this package, every `middleware/*` and every `sink/*` — benchmarked
against one shared discard control in one run, because a control measured in a
different run is not a control. The encoder half is `encoder/BENCH.md`.

Its headline: **the claim held at any middleware DEPTH and broke at fan-out
WIDTH.** `multi.Write` and `failover.Write` each pre-sized a per-record error
slate whose capacity was not a compile-time constant, so from three branches up
they heap-allocated on every record on the completely healthy path — a
`recover`→`failover(4)`→`multi(4)` emit measured 3 allocs/op. Both now declare
the slate nil, and every stack in the report is back to **1 alloc/op** at every
depth and width. `TestV116BuildSendAllocatesOnePerEmit` and
`TestT34TraceCorrelationAddsNoAllocation` were not touched and still pass.

The report also splits where an emit's time actually goes: the encoder was
46 %, `runtime.Callers` (the caller-PC capture in `Send`) is 22.5 %,
`mergeAttrs` — the one allocation — is 6.2 %, and the sink chain is under 1 %.
The middleware costs matter for their allocation behaviour far more than for
their nanoseconds.

## Verification

```
bazel test --config=race //internal/service/logger:logger_test
bazel test --config=race //internal/service/logger/...
```

Benchmarks (never under `-race`; `async` is concurrent and its timings are
meaningless with the detector on):

```
cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s -count=3 ./logger/
```

## Subtree

- `encoder/` — `core/logger.Encoder` adapters (text today; ndjson/json later)
- `middleware/` — chainable `Sink` decorators (multi, async, route, failover, sample, recover)
- `sink/` — terminal `Sink` implementations (console, file, syslog)

## Accepted audit findings

- Deferred/accepted low+info audit findings (V27) are recorded in `.claude/contexts/sdk-audit-2026-06-03-accepted.yaml` (2026-06-03 close-out). Each is a deliberate decision or deferred change, not an open bug.
