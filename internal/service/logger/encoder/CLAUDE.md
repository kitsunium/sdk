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
| `encoder.go`  | `Encoder = corelogger.Encoder` type alias |
| `text.go`     | `textEncoder` — renders `RecordEvent` → bytes, sanitises framing bytes through the exported `AppendSanitized` (the one scrub the legacy `service/logger.TextHandler` calls too), renders the top-level trace context; `appendQuotedString`/`quoteSafe` are its escaping fast path |
| `json.go`     | `jsonEncoder` — renders `RecordEvent` → one JSON object per line, flat attrs, top-level trace context |
| `timestamp.go`| `appendTimestamp` — the shared RFC3339-milli renderer BOTH encoders use in place of `time.Time.AppendFormat` |

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
- Reserved keys (ADR 0070): a top-level attribute named `trace_id` or `span_id`
  renders as `attr.trace_id` / `attr.span_id`. Renamed, never dropped. The whole
  `attr.` namespace is reserved with them, so a key already inside it is
  prefixed in turn (`attr.trace_id` → `attr.attr.trace_id`) — without that the
  rename is not injective and two caller keys collide where the SDK's field no
  longer does.
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
  Now measured for the whole package: **every benchmark in `BENCH.md` reports
  0 B/op and 0 allocs/op**, at 0/4/16 attributes, every `Kind`, with and
  without groups and trace context.
- **Two hot paths exist because two profiles ordered them**, and both are
  pinned by differential tests rather than by review. `appendQuotedString`
  (text.go) replaces `strconv.AppendQuote` when every byte is printable ASCII
  other than `"` and `\` — the set AppendQuote copies verbatim — which was
  **70 % of a text encode**, half of it `utf8.DecodeRuneInString` + IsPrint.
  `appendTimestamp` (timestamp.go) replaces `time.Time.AppendFormat` for the
  one layout both encoders declare, which was **34.6 % of a whole emit**
  because the generic formatter re-parses the layout per record; it is 5-6×
  faster and falls back to the stdlib for any year outside `[0, 9999]`.
  **Neither is allowed to differ by one byte**: `TestAppendQuotedStringMatchesStrconv`
  sweeps all 256 byte values in three positions, and
  `TestAppendTimestampMatchesAppendFormat` sweeps four zones × 100 000 instants.
  Change either fast path and those tests are the contract — widen an accepted
  set and they fail before a malformed log line ever reaches a parser.
- **One timestamp renderer, two encoders.** `timestampLayout` and
  `jsonTimestampLayout` are the same string, and `appendTimestamp` renders that
  one layout. `TestJSONTimestampLayoutMatchesTextLayout` asserts the equality,
  so giving JSON its own shape fails loudly instead of silently emitting the
  text encoder's format.

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
- Write a TOP-LEVEL attribute key without asking `ReservesKey` first. `trace_id`
  and `span_id` are the SDK's own fields, and a second member of that name lets
  a decoder keeping the last one read the caller's value as the line's
  correlation; the attribute renders as `attr.trace_id` instead (ADR 0070). A
  GROUPED key already carries its prefix and is left alone — asking there would
  rename `http.trace_id`, which collides with nothing.
- Call `strconv.AppendQuote` or `time.Time.AppendFormat` directly on the hot
  path again. Both are still reachable — as the documented FALLBACK inside
  `appendQuotedString` and `appendTimestamp` — but a new call site bypasses the
  measured fast paths and the differential tests that guard them.
- Widen `quoteSafe`'s accepted byte set or `appendTimestamp`'s year window
  without re-running the differential tests. They are not style checks; they
  are the only thing keeping a fast path honest.

## Verification

```
bazel test --config=race //internal/service/logger/encoder:encoder_test
```

Benchmarks and their reasoning live in `BENCH.md`; refresh with

```
cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s -count=3 ./logger/encoder/
```
