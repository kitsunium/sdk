<!-- updated: 2026-11-03T18:57:32Z -->
# internal/service/observe/metrics/goruntime/

## Purpose

The Go runtime's own instruments on a metrics meter (#868), under the
OpenTelemetry Go runtime semantic conventions: `RuntimeInstruments` registers
them as observable instruments on any `core/observe/metrics.AsyncMeter`, with
their units and descriptions where the meter keeps them.

## Why a package of its own

The instruments read `runtime/metrics`, and Go runs the initialisation of every
package a binary imports, used or not. `internal/service/observe/metrics` is in
`framework/app`'s import graph, and `TestTheFacadeImportsNoOptInSubsystem`
(ADR 0172, ADR 0179 §7) forbids `runtime/metrics` there: only
`framework/app/server` may bring it. Here, only a program that registers the
runtime's instruments pays for that package.

## Surface

| Symbol | What |
|---|---|
| `RuntimeInstruments(meter, cfg)` | `go.memory.used`, `go.memory.limit` (while a limit is set), `go.memory.allocated`, `go.memory.allocations`, `go.memory.gc.goal`, `go.goroutine.count`, `go.processor.limit`, `go.config.gogc` as observable sums; on an `AsyncHistogramMeter` also `go.schedule.duration` (`/sched/latencies:seconds`) and `go.gc.duration` (`/sched/pauses/total/gc:seconds`) as observable histograms in seconds, `NoSum` |
| `RuntimeConfig` | `Underscore` spells the names with `_` for a meter a Prometheus scrape reads |

## Why-this-shape

- **One read per collection.** Every instrument reads from ONE
  `runtime/metrics.Read` into samples allocated at registration: the meter runs
  each callback once per `Collect`, so the first callback of a collection reads
  for all of them (`runtimeSamples.serve`).
- **The runtime's buckets, not ours.** The two histograms carry the runtime's
  161 inner edges as the explicit bounds, its -Inf and +Inf edges as the
  implicit first and overflow buckets, converted into buffers allocated on the
  first read and reused while the layout stays the same
  (`runtimeHistogram.convert`). `NoSum`: the runtime keeps no total.
- **Opt-in, no attributes.** Nothing is registered unless the product asks; the
  Resource carries the service.

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from `design/observe/metrics.yaml` |
| `runtime.go` | `RuntimeInstruments`, `RuntimeConfig`; `runtimeSamples` (the shared read), `runtimeInstrument`, `runtimeHistogram` and its `convert`, `runtimeNotes` (names and annotations) |
| `runtime_external_test.go` | every instrument under its OpenTelemetry name, kind and unit, through the OTLP/JSON, text and Prometheus exporters; the underscored names; the two histograms with the runtime's buckets and no sum; `BenchmarkCollect_Runtime` |
| `BENCH.md` | the cost of a collection of the runtime instruments |

## Do NOT

- Move these instruments back into `internal/service/observe/metrics`, or make
  that package import this one: `framework/app` would pay `runtime/metrics`.
- Read `runtime/metrics` once per instrument: one `Read` serves a collection.

## Verification

```
cd internal && go test -race ./service/observe/metrics/goruntime/
cd internal && go test -run '^$' -bench Collect_Runtime -benchmem ./service/observe/metrics/goruntime/
```
