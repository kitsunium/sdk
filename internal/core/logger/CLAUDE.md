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
| `record.go` | `RecordEvent` — `Time`, `Level`, `Message`, `PC uintptr`, `Attrs []AttrValue`, `TraceContext TraceContextValue` |
| `trace_context.go` | `TraceContextValue{TraceID [16]byte; SpanID [8]byte}` + `IsValid()` + `AppendTraceIDHex` / `AppendSpanIDHex`; the `TraceContextSource func(ctx) TraceContextValue` port; `TraceIDKey`/`SpanIDKey` (`"trace_id"`/`"span_id"`) and the four length constants (ADR 0062) |
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
- **No `context` import outside interface signatures**; the package stays mockable without runtime stubs. `TraceContextSource` is a func port, so it is a signature too.
- **Trace correlation is a FIELD of the record, never two attributes** (ADR 0062). Two reasons: `Encoder.Append` receives no `context.Context`, so the identity has to travel on the record to reach a formatter at all; and OpenTelemetry prescribes `trace_id` / `span_id` as **top-level keys** of the log object (`specification/compatibility/logging_trace_context.md`), which an attribute cannot be — `WithGroup("http")` would render it as `http.trace_id`.
- **The zero `TraceContextValue` renders NOTHING.** Not `trace_id=""`, not 32 zeroes: an all-zero identifier is invalid under W3C Trace Context §3.2.2.3/§3.2.2.4, and emitting one would put an unjoinable field on every line a service logs outside a request. `IsValid()` requires BOTH identifiers and is the single gate all three formatters consult.
- **`AppendTraceIDHex` / `AppendSpanIDHex` append and never return a string.** A string is a heap allocation, and the logger's budget is one allocation per emit — already spent on the handler's attrs clone. They live here rather than in `service/logger` because three formatters (text encoder, JSON encoder, legacy `TextHandler`) must produce the same bytes. This is the one deliberate exception to "no runtime behaviour here": it is a pure function over an immutable value, the same category as `Value.String()` and `Kind.String()`.
- **`TraceContextValue` re-declares its two arrays** instead of aliasing `core/trace.TraceID`/`SpanID`, so this package stays stdlib-only. The binding lives in `pkg/v1/logger`, and the array conversion it performs is also the compile-time proof that the two domains agree on the widths.

## Do NOT

- Add concrete types with runtime behaviour here. Even a default `nopHandler` belongs in `internal/service/logger`.
- Add a `Builder` chainable here. The chainable record builder is a `pkg/v1/logger` ergonomic helper — keeping it out of core lets handlers stay generic.
- Grow `RecordEvent` with sink-specific metadata (CloudWatch tags, syslog facility); sinks read what they need from the record + ctx directly. `TraceContext` is NOT an exception being carved out: it is not sink-specific, every formatter needs it, and the `Encoder` port has no other way to receive it (ADR 0062).
- Import `internal/core/trace` from here. The trace domain's model — and `core/metrics` behind it — would then sit in front of every consumer who wants a line on stderr. The bridge is `pkg/v1/logger/tracecontext.go`, and it is the only place the two domains meet.
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

## Accepted audit findings

- Deferred/accepted low+info audit findings (V13, V15) are recorded in `.claude/contexts/sdk-audit-2026-06-03-accepted.yaml` (2026-06-03 close-out). Each is a deliberate decision or deferred change, not an open bug.
