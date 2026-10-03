<!-- updated: 2026-10-03T05:20:00Z -->
# internal/core/observe/logger/

## Purpose

Interface layer of the SDK's structured logger. Defines the four ports between the caller-facing `Logger`, the formatting `Handler`, the format-side `Encoder` and the transport-side `Sink`, plus the immutable value types they carry. No runtime behaviour lives here — concrete implementations sit under `internal/service/observe/logger/{encoder,sink,middleware,writer}/`. This directory carries no `README.md`: the human-readable surface doc is the public facade's generated `pkg/v1/observe/logger/README.md`, and this file captures the engineering rules.

Code range: `0.2.1.*` reserved (ADR 0005 §Registry); `codeRangeOwners` carries no entry for it. No codes emitted today — service-layer wiring owns the slot.

## Contents

| File | Surface |
|---|---|
| `logger.go` | `Logger` — `Log(ctx, level, msg, attrs...)`, `With(attrs...) Logger`, `WithGroup(name) Logger`, `Enabled(ctx, level) bool` |
| `handler.go` | `Handler` — `Enabled(ctx, RecordEvent) bool`, `Handle(ctx, RecordEvent) error`, `WithAttrs([]AttrValue) Handler`, `WithGroup(name) Handler` |
| `encoder.go` | `Encoder` (format-side port) — `Name() string`, `Append(dst, groups, RecordEvent) []byte` |
| `sink.go` | `Sink` (transport-side port) — `Write(ctx, RecordEvent, []byte) (int, error)`, `Flush(ctx) error`, `Close() error` |
| `record.go` | `RecordEvent` — `Time`, `Level`, `Message`, `PC uintptr`, `Attrs []AttrValue`, `TraceContext TraceContextValue` — and `AttrValue{Key string; Value Value}` |
| `trace_context.go` | `TraceContextValue{TraceID [16]byte; SpanID [8]byte}` + `IsValid()` + `AppendTraceIDHex` / `AppendSpanIDHex`; the `TraceContextSource func(ctx) TraceContextValue` port; `TraceIDKey`/`SpanIDKey` (`"trace_id"`/`"span_id"`) and the four length constants (ADR 0062) |
| `value.go` | `Value` discriminated union + typed constructors (`StringValue` / `Int64Value` / `IntValue` / `Uint64Value` / `Float64Value` / `BoolValue` / `DurationValue` / `TimeValue` / `GroupValue` / `AnyValue`, and `NewValue`, an alias of `AnyValue`) and accessors |
| `kind.go` | `Kind int8` + `KindAny`/`KindBool`/`KindDuration`/`KindFloat64`/`KindInt64`/`KindString`/`KindTime`/`KindUint64`/`KindGroup` + `String()` |

Sub-packages, beneath this one because they are the logger's own (ADR 0155):

- `level/` — severity constants (`Debug`/`Info`/`Warn`/`Error`); see `level/CLAUDE.md`.
- `writer/` — the registry of named, config-driven factories that yield this package's `Sink` (ADR 0012); see `writer/CLAUDE.md`. It imports this package and `level/`; this package imports `level/` and never `writer/`.

## Conventions

- **Two ports per side.** `Handler` straddles the line between caller and transport. `Encoder` (format) and `Sink` (transport) are independent ports — the service-layer `genericHandler` composes one of each.
- **Copy-on-write everywhere.** `With` / `WithAttrs` / `WithGroup` MUST return a derived value without mutating the receiver. Handlers that miss this break `slog`-style contextual logging.
- **`RecordEvent.Time` may be zero**; handlers MUST supply a fallback (typically via `kernel/clock.System.Now()`).
- **`Enabled` is the fast-path gate**; implementations SHOULD short-circuit on a cancelled context so the hot path never builds attrs that will be dropped.
- **`Value` is bit-packed.** `bool` / `int64` / `uint64` / `float64` / `time.Duration` share the `bits packedBits` field; `string` lives in `str`; `time.Time` / group slice / opaque `any` payload live in `any`. Typed accessors are undefined when called against the wrong `Kind` — callers MUST guard with `Kind()` first. `Value.String()` is the one defensive exception (returns `""` for non-string Kinds).
- **Role-suffix** on exported structs: `AttrValue`, `RecordEvent`. `pkg/v1/observe/logger` re-exports the short forms via Go type aliases.
- **No `context` import outside interface signatures**; the package stays mockable without runtime stubs. `TraceContextSource` is a func port, so it is a signature too.
- **Trace correlation is a FIELD of the record, never two attributes** (ADR 0062). Two reasons: `Encoder.Append` receives no `context.Context`, so the identity has to travel on the record to reach a formatter at all; and OpenTelemetry prescribes `trace_id` / `span_id` as **top-level keys** of the log object (`specification/compatibility/logging_trace_context.md`), which an attribute cannot be — `WithGroup("http")` would render it as `http.trace_id`.
- **The zero `TraceContextValue` renders NOTHING.** Not `trace_id=""`, not 32 zeroes: an all-zero identifier is invalid under W3C Trace Context §3.2.2.3/§3.2.2.4, and emitting one would put an unjoinable field on every line a service logs outside a request. `IsValid()` requires BOTH identifiers and is the single gate all three formatters consult.
- **`AppendTraceIDHex` / `AppendSpanIDHex` append and never return a string.** A string is a heap allocation, and the logger's budget is one allocation per emit — already spent on the handler's attrs clone. They live here rather than in `service/observe/logger` because three formatters (text encoder, JSON encoder, legacy `TextHandler`) must produce the same bytes. This is the one deliberate exception to "no runtime behaviour here": it is a pure function over an immutable value, the same category as `Value.String()` and `Kind.String()`.
- **`TraceContextValue` re-declares its two arrays** instead of aliasing `core/observe/trace.TraceID`/`SpanID`, so this package stays stdlib-only. The binding lives in `pkg/v1/observe/logger`, and the array conversion it performs is also the compile-time proof that the two domains agree on the widths.

## Do NOT

- Add concrete types with runtime behaviour here. Even a default `nopHandler` belongs in `internal/service/observe/logger`.
- Add a `Builder` chainable here. The chainable record builder is a `pkg/v1/observe/logger` ergonomic helper — keeping it out of core lets handlers stay generic.
- Grow `RecordEvent` with sink-specific metadata (CloudWatch tags, syslog facility); sinks read what they need from the record + ctx directly. `TraceContext` is NOT an exception being carved out: it is not sink-specific, every formatter needs it, and the `Encoder` port has no other way to receive it (ADR 0062).
- Import `internal/core/observe/trace` from here. The trace domain's model — and the shared `core/observe/otel` model behind it — would then sit in front of every consumer who wants a line on stderr. The bridge is `pkg/v1/observe/logger/tracecontext.go`, and it is the only place the two domains meet.
- Expose a `Value.UnmarshalAny` reflection helper — `Value` is a write-once payload and the typed accessors are the contract.

## Verification

```
# Primary (Bazel)
bazel test --config=race //internal/core/observe/logger:logger_test

# Fallback
cd internal/core && GOWORK=off go test -race -cover ./observe/logger/...
# expected: kind String() lookup covered, value pack/unpack round-trip
# (internal + external), interface files have no tests by design.
```

Contract is exercised end-to-end by `internal/service/observe/logger` and `pkg/v1/observe/logger` test suites.
