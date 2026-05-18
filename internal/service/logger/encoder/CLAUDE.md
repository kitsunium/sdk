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
| `text.go`    | `textEncoder` — renders `RecordEvent` → bytes, sanitises framing bytes |

## Output shape

```
2026-04-19T12:34:56.789Z INFO message g1.g2.key="quoted val" k=42 d="500ms"\n
```

- Timestamp: RFC3339 with millisecond precision (`timestampLayout`).
- Level: `level.Level.String()` (uppercase).
- Message: sanitised — `\n`, `\r`, NUL collapsed to a single space so
  line-framed downstream sinks (syslog RFC5424, plain file tail) cannot be
  spoofed by attacker-influenced content.
- Attrs: `key=value` with strings via `strconv.AppendQuote`; durations and
  times also quoted; ints / uints / bool / float64 unquoted; every other
  `Kind` degrades to `?` until structured encoders land.
- Group prefix: `g1.g2.…` joined by `groupSeparator` ('.').

## Conventions

- **Clock injection.** `NewText(clk)` accepts a `clock.Clock`; nil falls
  back to `clock.System`. Tests inject a fake clock for deterministic time.
- **IFACE-PLUGIN.** `NewText` returns the `Encoder` interface — the
  concrete `textEncoder` is unexported on purpose.
- **Zero-alloc steady state.** Encoders write into the caller's `dst` slice
  (typically borrowed from `kernel/buffer`); no internal allocations on the
  hot path beyond `strconv.Append*` growth.

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

## Verification

```
bazel test --config=race //internal/service/logger/encoder:encoder_test
```
