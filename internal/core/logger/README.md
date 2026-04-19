# `internal/core/logger`

**Layer**: core · **Code range**: 2100-2199 (reserved, no emissions today)

Interfaces-only layer: defines the contract between a `Logger` (caller-facing API) and a `Handler` (the sink that renders and writes). No concrete implementation lives here — implementations sit in `internal/service/logger`.

## Surface

```go
// Structured key/value pair attached to a RecordEvent.
type AttrValue struct {
    Key   string
    Value any
}

// Immutable snapshot of a single log event.
type RecordEvent struct {
    Time    time.Time
    Level   level.Level
    Message string
    Attrs   []AttrValue
}

// Formats and emits RecordEvents. Safe for concurrent use.
type Handler interface {
    Enabled(ctx context.Context, r RecordEvent) (enabled bool)
    Handle(ctx context.Context, r RecordEvent)  (err error)
    WithAttrs(attrs []AttrValue)                (child Handler)
}

// Primary logger interface. Safe for concurrent use.
type Logger interface {
    Log(ctx context.Context, lv level.Level, msg string, attrs ...AttrValue)
    With(attrs ...AttrValue)                     (child Logger)
    Enabled(ctx context.Context, lv level.Level) (enabled bool)
}
```

## Contract

- `RecordEvent.Time` may be the zero value; handlers MUST supply a fallback (typically via `kernel/clock.System.Now()`).
- `Handler.Handle` returns `error` so implementations can surface wire failures; `Logger.Log` returns nothing — loggers MUST NOT panic and SHOULD drop errors silently (service-layer `loggerImpl` does this).
- `WithAttrs` / `With` MUST return a derived handler/logger without mutating the receiver. Copy-on-write is the expected pattern.
- `Enabled` is the fast-path gate; callers can skip expensive attribute construction when it returns false. Implementations SHOULD short-circuit on a cancelled context.

## Do NOT

- Add concrete types here. Any struct or function with runtime behaviour belongs in `internal/service/logger`.
- Depend on `internal/service/*` — core must be importable by the service layer, not the other way round.
- Import `context` outside the interface signatures; core stays interface-only so generators / mocks stay trivial.

## Role-suffix types

Types are suffixed `Value` / `Event` to satisfy the ktn-linter `KTN-STRUCT-ROLE` rule. `pkg/v1/logger` re-exports them as `Attr` (clean public name) via Go type aliases.

## Tests

No tests in this package — it's interface-only. Contract is exercised end-to-end by `internal/service/logger` and `pkg/v1/logger` test suites.
