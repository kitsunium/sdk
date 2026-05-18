<!-- updated: 2026-05-18T14:30:00Z -->
# internal/core/logger/

## Purpose

Interface layer of the SDK's structured logger. Defines the four ports between the caller-facing `Logger`, the formatting `Handler`, the format-side `Encoder` and the transport-side `Sink`, plus the immutable value types they carry. No runtime behaviour lives here — concrete implementations sit under `internal/service/logger/{encoder,sink,middleware}/`. The human-readable surface doc is `README.md`; this file captures the engineering rules.

Code range: `0.2.16.*` reserved (ADR 0006). No codes emitted today — service-layer wiring owns the slot.

## Contents

| File | Surface |
|---|---|
| `logger.go` | `Logger` — `Log(ctx, level, msg, attrs...)`, `With(attrs...) Logger`, `WithGroup(name) Logger`, `Enabled(ctx, level) bool` |
| `handler.go` | `Handler` — `Enabled(ctx, RecordEvent) bool`, `Handle(ctx, RecordEvent) error`, `WithAttrs([]AttrValue) Handler`, `WithGroup(name) Handler` |
| `encoder.go` | `Encoder` (format-side port) — `Name() string`, `Append(dst, groups, RecordEvent) []byte` |
| `sink.go` | `Sink` (transport-side port) — `Write(ctx, RecordEvent, []byte) (int, error)`, `Flush(ctx) error`, `Close() error` |
| `record.go` | `RecordEvent` — `Time`, `Level`, `Message`, `PC uintptr`, `Attrs []AttrValue` |
| `attr.go` | `AttrValue{Key string; Value Value}` |
| `value.go` | `Value` discriminated union + typed constructors (`StringValue` / `Int64Value` / `Float64Value` / `BoolValue` / `DurationValue` / `TimeValue` / `GroupValue` / `AnyValue`) and accessors |
| `kind.go` | `Kind int8` + `KindAny`/`KindBool`/`KindDuration`/`KindFloat64`/`KindInt64`/`KindString`/`KindTime`/`KindUint64`/`KindGroup` + `String()` |

Sub-package: [`level/`](./level/) — severity constants (`Debug`/`Info`/`Warn`/`Error`).

## Conventions

- **Two ports per side.** `Handler` straddles the line between caller and transport. `Encoder` (format) and `Sink` (transport) are independent ports — the service-layer `genericHandler` composes one of each.
- **Copy-on-write everywhere.** `With` / `WithAttrs` / `WithGroup` MUST return a derived value without mutating the receiver. Handlers that miss this break `slog`-style contextual logging.
- **`RecordEvent.Time` may be zero**; handlers MUST supply a fallback (typically via `kernel/clock.System.Now()`).
- **`Enabled` is the fast-path gate**; implementations SHOULD short-circuit on a cancelled context so the hot path never builds attrs that will be dropped.
- **`Value` is bit-packed.** `bool` / `int64` / `uint64` / `float64` / `time.Duration` share the `bits packedBits` field; `string` lives in `str`; `time.Time` / group slice / opaque `any` payload live in `any`. Typed accessors are undefined when called against the wrong `Kind` — callers MUST guard with `Kind()` first. `Value.String()` is the one defensive exception (returns `""` for non-string Kinds).
- **Role-suffix** on exported structs: `AttrValue`, `RecordEvent`. `pkg/v1/logger` re-exports the short forms via Go type aliases.
- **No `context` import outside interface signatures**; the package stays mockable without runtime stubs.

## Do NOT

- Add concrete types with runtime behaviour here. Even a default `nopHandler` belongs in `internal/service/logger`.
- Add a `Builder` chainable here. The chainable record builder is a `pkg/v1/logger` ergonomic helper — keeping it out of core lets handlers stay generic.
- Grow `RecordEvent` with sink-specific metadata (CloudWatch tags, syslog facility); sinks read what they need from the record + ctx directly.
- Expose a `Value.UnmarshalAny` reflection helper — `Value` is a write-once payload and the typed accessors are the contract.

## Verification

```
# Primary (Bazel)
bazel test --config=race //internal/core/logger:logger_test

# Fallback
cd internal/core && GOWORK=off go test -race -cover ./logger/...
# expected: kind String() lookup covered, value pack/unpack round-trip
# (internal + external), interface files have no tests by design.
```

Contract is exercised end-to-end by `internal/service/logger` and `pkg/v1/logger` test suites.
