<!-- updated: 2026-10-03T05:20:00Z -->
# internal/service/observe/

## Purpose

The observe family's engines (ADR 0155): the concrete halves of the contracts
under `internal/core/observe` — the logger and its writers, metrics, tracing —
and the process's own profiles. This directory holds no Go code: it is a
prefix, not a package, and nothing imports `internal/service/observe` itself.
Each member is a package of the `internal/service` module with its own
`CLAUDE.md`, and each facade sits at the same path under `pkg/v1/observe`.

## The rule that put them together

A domain belongs here when what it produces is the process's account of
ITSELF, kept for somebody other than the caller of the call being made: a line
that says what happened (`logger`), a number that says how much (`metrics`), a
span that says how long and on whose behalf (`trace`), and the profiles that
say where the time and the memory went (`profiling`). `health` is not here:
readiness and liveness answer a question a supervisor asks, and belong to the
app family.

The logger's writers sit beneath the logger, at `logger/writer/`, because their
registry is the logger's (`internal/core/observe/logger/writer`) and the `Sink`
each one builds is the logger's. The two helpers the engines share sit beside
their importers, where Go's `internal/` rule confines them to exactly those:
`internal/otlp/` for the two signals that speak OTLP, `logger/internal/logfile/`
for the two file sinks.

## Members

| Package | What it is | Built on | Code range | Facade |
|---|---|---|---|---|
| `logger/` | the one-allocation-per-emit logger: the handlers, the `encoder/` text and JSON formats, the `middleware/*` decorators (`multi`, `async`, `route`, `failover`, `sample`, `recover`, `encwrite`, `tee`) and the `sink/*` terminals (`console`, `file`, `syslog`, `memory`); trace correlation is injected through `NewWithTraceContext` and never imported (ADR 0062) | `core/observe/logger` | `0.3.1.*`, and one slot per sink and middleware (`logger/CLAUDE.md`) | `pkg/v1/observe/logger` |
| `logger/writer/` | the log-transport writers behind the `core/observe/logger/writer` registry: `console`, `file`, `rotfile`, `journald` and `nettransport` register a factory, `dbsink` is the driver-agnostic database sink shell, `levelgate` the floor a writer's sink is wrapped in (ADR 0012, ADR 0015, ADR 0132) | `core/observe/logger/writer`, `logger/sink/*`, `logger/middleware/async` | `rotfile` `0.3.27.*`, `nettransport` `0.3.30.*`, `journald` `0.3.31.*` | `pkg/v1/observe/logger/writer` (`console`, `file`, `rotfile`), `pkg/v1/observe/logger` (`LevelGate`); `dbsink` through `third-party/db/writer/*` |
| `metrics/` | the in-memory `Meter` and its lock-free instruments, the `text` exporter, the lossy `prometheus` connector, and OTLP/JSON with the OTLP/HTTP emitter (ADR 0027, ADR 0044, ADR 0048) | `core/observe/metrics`, `core/observe/otel`, `internal/otlp` | `0.3.45.*` (+ core `0.2.9.*`) | `pkg/v1/observe/metrics` |
| `trace/` | the `Tracer`, four samplers, the in-memory `Recorder`, `RecordError`, OTLP/JSON with the OTLP/HTTP emitter, and the server and client HTTP middlewares (ADR 0051) | `core/observe/trace`, `core/observe/otel`, `internal/otlp` | `0.3.50.*` (+ core `0.2.20.*`) | `pkg/v1/observe/trace` |
| `profiling/` | a bounded CPU window and the live heap on `runtime/pprof`, the pprof format decoded with the standard library, `Fold` onto owners the caller names, goroutine dumps read and grouped; no core yet (ADR 0121 §D1, ADR 0160 §1) | none | `0.3.89.*` | `pkg/v1/observe/profiling` |
| `internal/otlp/` | the OTLP machinery `metrics` and `trace` share — proto3-JSON scalars, the `common` and `resource` messages, the marshal, the NDJSON stream, the OTLP/HTTP sender; it owns no code, each signal hands it a `SignalSpec` (ADR 0048, ADR 0051) | `core/observe/otel` | none — the calling signal's | none |
| `logger/internal/logfile/` | the hardened open both file sinks share — an `os.Lstat` refusal, then `O_NOFOLLOW` on every Unix (CWE-59); it owns no code, each sink hands it a `RefusalSpec` | none | none — the calling sink's (`0.3.14.*`, `0.3.27.*`) | none |

Outside the family the engines reach contracts only: `logger/middleware/encwrite`
seals through `core/crypto`, `logger/writer/journald` refuses with
`core/proc`'s `UnsupportedPlatform`, and `trace`'s middlewares are `core/net`'s
middleware type. Inside it, `logger` keeps zero edges to `trace` and `metrics`
(ADR 0062), the writers compose the logger's sinks, `logger/middleware/async`
and `logger/writer/levelgate`, and `profiling` imports nothing of the family.

Every range above kept its value when its package moved here from the root of
`internal/service` (ADR 0160): `codeRangeOwners` names the new directories
under the same keys, and `//:audit_sources` lists them by their new labels.

## Do NOT

- Put Go code in this directory. A file here would make `observe` a service
  package of its own, with no contract above it.
- Import `trace` or `metrics` from `logger`, or `logger` from a signal: the
  only place logging and tracing meet is the public facade (ADR 0062).
- Move `internal/otlp` or `logger/internal/logfile` up to widen who may import
  them. Each sits where Go's `internal/` rule admits exactly its importers; a
  third importer is a reason to look at the design, not at the directory.
- Renumber a member's codes to match its new path (ADR 0160).
