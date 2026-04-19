# `internal/service/logger`

**Layer**: service · **Code range**: 3100-3199

Concrete implementation of the `core.Handler` + `core.Logger` interfaces. Consumers generally import `pkg/v1/logger` instead of this package — `pkg/v1/logger` decorates the output with `framework_version` and hides the construction wiring.

## Surface

```go
func NewTextHandler(w io.Writer, min level.Level) (*TextHandler, error)
func New(h corelogger.Handler)                   (corelogger.Logger, error)

type TextHandler struct { /* unexported */ }
func (*TextHandler) Enabled(ctx, r)  bool
func (*TextHandler) Handle(ctx, r)   error
func (*TextHandler) WithAttrs(attrs) corelogger.Handler
```

## Error catalogue

| Code | Var            | When it fires                                                   | HTTP / Exit |
|------|----------------|-----------------------------------------------------------------|-------------|
| 3101 | `WriterNil`    | `NewTextHandler(nil, …)`                                        | 500 / 70    |
| 3102 | `HandlerNil`   | `New(nil)`                                                      | 500 / 70    |
| 3110 | `CtxCancelled` | `Handle` called with a cancelled context (wraps `context.Canceled`) | 500 / 70 |
| 3120 | `WriteFailed`  | Underlying `io.Writer.Write` returned an error (wraps the cause) | 500 / 74 EX_IOERR |

All four are declared in `errors.go` + `codes.go` and enforce the convention `Go var name == Reason`.

## Output format

Every record renders as a single line:

```
2026-04-19T12:34:56.789Z INFO message key1="string val" key2=42 key3=true key4=0.5
```

- Timestamp in RFC3339 with millisecond precision.
- Level as uppercase label via `kernel/level.Level.String()`.
- Attrs serialised `key=value`, with strings quoted, numerics / booleans / floats unquoted, unknown types rendered as `?`.
- Handler-bound attrs (via `WithAttrs`) come BEFORE record-attached attrs.

## Typical use

```go
h, err := svclogger.NewTextHandler(os.Stderr, level.Info)
if err != nil { /* handle */ }

lg, err := svclogger.New(h)
if err != nil { /* handle */ }

lg.Log(ctx, level.Warn, "disk almost full",
    corelogger.AttrValue{Key: "free_pct", Value: 3.2})
```

In practice call sites should prefer `pkg/v1/logger.NewText(Config{Writer: os.Stderr})` which does the same wiring and injects the framework version.

## Concurrency

- `TextHandler` serialises every write through an internal `sync.Mutex`; concurrent `Handle` calls produce atomic lines.
- `WithAttrs` is copy-on-write: derived handlers share the writer, own their `attrs` slice.
- `loggerImpl` is stateless beyond its `Handler` reference — `With` clones through `Handler.WithAttrs`.

## Context semantics

Both `Enabled` and `Handle` honour a cancelled `ctx`:
- `Enabled` returns `false` (the record is skipped).
- `Handle` returns a `CtxCancelled` wrapped error. `errors.Is(err, context.Canceled)` holds AND `errs.HasReason(err, "CTX_CANCELLED")` is `true` — the cause is preserved through Unwrap.

## Do NOT

- Call `Handle` directly from application code; go through a `Logger` so `Enabled` can short-circuit.
- Reuse a `RecordEvent` across goroutines after passing it to `Handle` — handlers may set `RecordEvent.Time` in place if it was the zero value.
- Expect nil safety on `*TextHandler`; `NewTextHandler` is the only valid constructor.

## Tests

- `text_handler_external_test.go` — NewTextHandler, Enabled, Handle (including cancelled context + concurrent write), WithAttrs isolation.
- `text_handler_internal_test.go` — `appendAttr` value rendering, `renderLine` composition, `writeLine` error wrapping, const sanity checks.
- `logger_external_test.go` — `New` rejects nil Handler, exposes HandlerNil.
- `logger_internal_test.go` — `loggerImpl.Enabled / Log / With` whitebox.

Coverage: 100%.
