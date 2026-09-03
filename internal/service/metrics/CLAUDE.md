# internal/service/metrics/

## Purpose

In-memory `Meter` + lock-free instruments (`Counter`/`Gauge`/`Histogram`)
implementing `core/metrics`, plus a stdlib **text** Exporter (registered to
**stderr** on import — ADR 0030). Stdlib-only, cross-OS. ADR 0027. Emits core
sentinels `0.2.9.*`.

## Contents

| File | Surface |
|---|---|
| `meter.go` | `memMeter` (RWMutex map of instruments) + `NewMeter` + `Collect` |
| `counter.go` | `memCounter` (atomic.Int64) |
| `gauge.go` | `memGauge` (atomic float64 bits, CAS) |
| `histogram.go` | `memHistogram` (sorted buckets + atomic counts/sum) |
| `kind.go` | instrument-kind guard (`assertKind` → InstrumentKindConflict) |
| `exporter_text.go` | `textExporter` + default **stderr** `Text` + `NewTextExporter` |

## Conventions

- **Lock-free instruments**; the meter's RWMutex guards only creation + Collect.
- **Registration via `var Text = metrics.RegisterExporter(...)`** — no `init()`.
- **The registered default writes to `os.Stderr`** (ADR 0030). Importing a
  package must not arm a writer on a stream the process may be using as a
  protocol channel; stdout is reachable only by asking for it explicitly with
  `NewTextExporter(name, os.Stdout)`.
- Instruments are idempotent by name; a cross-kind reuse panics.
- Cross-OS: 100 % portable (sync/atomic/math).

## Do NOT

- Discard writer errors — the text exporter buffers into `[]byte` then does one
  `Write` with a wrapped `EXPORT_FAILED` on failure.
- Add an `init()`.
- Point a *registered* exporter at `os.Stdout`. The import is invisible at the
  call site, so the default must be the stream nobody parses.

## Verification

```
bazel test --config=race //internal/service/metrics:metrics_test
```
