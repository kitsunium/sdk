<!-- updated: 2026-11-03T18:57:32Z -->
# pkg/v1/observe/metrics/goruntime/

## Purpose

The public facade of `internal/service/observe/metrics/goruntime` (#868): the
Go runtime's instruments on a metrics meter, with the OpenTelemetry Go runtime
conventions' names, kinds and units. Pure forwarders (`gen_facade.go`, kit's,
from `design/observe/metrics.yaml`); the README is generated
(`make docs-readme`).

It is not part of `observe/metrics` because it reads `runtime/metrics`, whose
initialisation every importer pays, and `framework/app` imports
`observe/metrics` (ADR 0172): only a program that registers the runtime's
instruments imports this package.

## Surface

| Symbol | Kind | What |
|---|---|---|
| `RuntimeInstruments(meter, cfg)` | func | `go.memory.*`, `go.goroutine.count`, `go.processor.limit`, `go.config.gogc`, and on an `AsyncHistogramMeter` `go.schedule.duration` and `go.gc.duration`, read at `Collect` from one `runtime/metrics.Read` |
| `RuntimeConfig` | alias | `Underscore` names them for a Prometheus meter |

## Do NOT

- Add the instruments' logic here: it is the internal package's.
- Re-export them from `observe/metrics`: that would bring `runtime/metrics`
  back into `framework/app`.
