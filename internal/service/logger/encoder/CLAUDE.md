<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/logger/encoder/

## Purpose

Concrete `core/logger.Encoder` adapters — format-only, transport-agnostic.
The interface itself lives in `internal/core/logger` (`encoder.go` re-
exports it as a type alias for legacy call sites). Today this package ships
the `text` encoder; `ndjson` / `json` land in follow-up commits.

## Contents

| File      | Role |
|---|---|
| `encoder.go` | `Encoder = corelogger.Encoder` type alias |
| `text.go`    | `textEncoder` — renders `RecordEvent` → bytes, sanitises framing bytes, renders the top-level trace context |
| `json.go`    | `jsonEncoder` — renders `RecordEvent` → one JSON object per line, flat attrs, top-level trace context |

## Output shape

```
2026-04-19T12:34:56.789Z INFO message trace_id=4bf92f3577b34da6a3ce929d0e0e4736 span_id=00f067aa0ba902b7 g1.g2.key="quoted val" k=42 d="500ms"\n
```

- Timestamp: RFC3339 with millisecond precision (`timestampLayout`).
- Level: `level.Level.String()` (uppercase).
- Message: sanitised — `\n`, `\r`, NUL collapsed to a single space so
  line-framed downstream sinks (syslog RFC5424, plain file tail) cannot be
  spoofed by attacker-influenced content.
- Attrs: `key=value` with strings via `strconv.AppendQuote`; durations and
  times also quoted; ints / uints / bool / float64 unquoted; every other
  `Kind` degrades to `?` until structured encoders land. The attribute **key**
  runs through the same framing-byte scrub as the Message, so an
  attacker-influenced key cannot inject a frame boundary (V110).
- Group prefix: `g1.g2.…` joined by `groupSeparator` ('.'); each group **name**
  segment is also framing-byte-scrubbed (V110).
- Trace context (ADR 0062): `trace_id=<32 lowercase hex> span_id=<16 lowercase hex>`,
  emitted between the header and the attributes, **unquoted** (they are record
  fields rather than attribute values, are fixed-length hex, and an operator
  greps for the id exactly as the tracing backend shows it), and **never**
  carrying the group prefix — OpenTelemetry requires them to be top-level keys.
  When `RecordEvent.TraceContext` is the invalid zero value **nothing at all is
  written**: no key, no empty value, no all-zero identifier.

## Conventions

- **Clock injection.** `NewText(clk)` accepts a `clock.Clock`; nil falls
  back to `clock.System`. Tests inject a fake clock for deterministic time.
- **IFACE-PLUGIN.** `NewText` returns the `Encoder` interface — the
  concrete `textEncoder` is unexported on purpose.
- **Zero-alloc steady state.** Encoders write into the caller's `dst` slice
  (typically borrowed from `kernel/buffer`); no internal allocations on the
  hot path beyond `strconv.Append*` growth. The trace identifiers hold to it:
  `TraceContextValue.Append*Hex` `hex.Encode`s into a stack array and appends,
  so no Go string is ever materialised — profiled in `pkg/v1/logger/BENCH.md`.

## Error catalogue — range 0.3.2.\*

This package owns the **0.3.2.\*** slot per ADR 0006, but ships no
sentinels today — encoding never fails (unknown `Kind` degrades to `?`).
A future structured encoder may populate the range.

## Do NOT

- Acquire any I/O resource here — encoders are pure format adapters.
- Mutate the input `RecordEvent` beyond filling a zero-`Time` field via
  the injected clock.
- Re-introduce framing-sensitive bytes in the output without updating the
  sanitiser contract; sinks rely on it.
- Render the trace context through `appendAttrWithGroups` / `appendJSONAttr`.
  It is a top-level field: a group prefix on it produces `http.trace_id`, which
  no ingestion pipeline recognises. There is a named test for that.
- Render an absent trace context as an empty or all-zero id — see ADR 0062 §3.

## Verification

```
bazel test --config=race //internal/service/logger/encoder:encoder_test
```
