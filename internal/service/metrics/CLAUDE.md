# internal/service/metrics/

## Purpose

In-memory `Meter` + lock-free instruments (`Counter`/`Gauge`/`Histogram`)
implementing `core/metrics`, plus a stdlib **text** Exporter (registered to
stdout on import). Stdlib-only, cross-OS. ADR 0027. Emits core sentinels `0.2.9.*`.

## Contents

| File | Surface |
|---|---|
| `meter.go` | `memMeter` (RWMutex map of instruments) + `NewMeter` + `Collect` |
| `counter.go` | `memCounter` (atomic.Int64) |
| `gauge.go` | `memGauge` (atomic float64 bits, CAS) |
| `histogram.go` | `memHistogram` (sorted buckets + atomic counts/sum) |
| `kind.go` | instrument-kind guard (`assertKind` → InstrumentKindConflict) |
| `exporter_text.go` | `textExporter` + default stdout `Text` + `NewTextExporter` |

## Conventions

- **Lock-free instruments**; the meter's RWMutex guards only creation + Collect.
- **Registration via `var Text = metrics.RegisterExporter(...)`** — no `init()`.
- Instruments are idempotent by name; a cross-kind reuse panics.
- Cross-OS: 100 % portable (sync/atomic/math).

## Do NOT

- Discard writer errors — the text exporter buffers into `[]byte` then does one
  `Write` with a wrapped `EXPORT_FAILED` on failure.
- Add an `init()`.

## Verification

```
bazel test --config=race //internal/service/metrics:metrics_test
```
