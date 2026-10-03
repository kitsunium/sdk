<!-- updated: 2026-10-03T05:20:00Z -->
# internal/core/observe/

## Purpose

The observe family's contracts (ADR 0155): the ports, the values and the codes
of the signals a process emits about itself. This directory holds no Go code:
it is a prefix, not a package, and nothing imports `internal/core/observe`
itself. Each member is a package of the `internal/core` module with its own
`CLAUDE.md`, and each still follows the core's rules — interfaces and immutable
values, the standard library and the kernel only (`internal/core/CLAUDE.md`).
The engines are the same paths under `internal/service/observe`, and the
facades the same paths under `pkg/v1/observe`.

## The rule that put them together

A domain belongs here when what it carries is the process's account of
ITSELF, kept for somebody other than the caller of the call being made: a line
that says what happened (`logger`), a number that says how much (`metrics`), a
span that says how long and on whose behalf (`trace`) — OpenTelemetry's three
signals — and, in the engine and facade layers, the profiles that say where the
time and the memory went (`profiling`). `otel` is here because it is what two
of the signals share: the attribute, resource and scope types of
OpenTelemetry's `common.proto`, extracted so that neither signal borrows the
other's model (ADR 0051 §Decision 2).

`health` is not here: readiness and liveness answer a question a supervisor
asks, and belong to the app family. Neither is an exporter's transport: the
OTLP wire is a mechanism, written once in the engines' own
`internal/service/observe/internal/otlp`.

The logger's two sub-packages sit beneath it rather than beside it, because
they are the logger's own: `level` is its severity vocabulary (it left the
kernel for failing the "generic" half of the kernel rule), and `writer` is the
registry of named factories whose product is the logger's `Sink` (ADR 0012) —
the logger is the only domain it serves.

`profiling`, the family's fourth domain, has no core package yet — its codes
are declared by its engine, `internal/service/observe/profiling`
(ADR 0121 §D1). ADR 0160 §1 gives it one, as `profiling/` beside the members
below, when the reorganisation series reaches it.

## Members

| Package | What it declares | Code range | Engine |
|---|---|---|---|
| `logger/` | the four ports — `Logger`, `Handler`, `Encoder`, `Sink` — the immutable `RecordEvent`, `AttrValue`, `Value` and `Kind`, and `TraceContextValue` with the `TraceContextSource` FUNC port; stdlib-only, the trace domain is NOT imported (ADR 0062); and the engine's codes and sentinels (ADR 0160) | `0.2.1.*`, reserved and empty; `0.3.1.*`, the engine's | `internal/service/observe/logger` |
| `logger/middleware/*`, `logger/sink/*` | the codes and sentinels of the engine's eight decorators (`async`, `encwrite`, `failover`, `multi`, `recover`, `route`, `sample`, `tee`) and three terminals (`console`, `file`, `syslog`) — one package per engine package, codes only (ADR 0160 §2) | `0.3.13.*`–`0.3.21.*`, `0.3.28.*`, `0.3.29.*` | `internal/service/observe/logger/{middleware,sink}/*` |
| `logger/level/` | `Level` and `Debug` / `Info` / `Warn` / `Error` | `0.2.17.*` | none — a vocabulary every logger package reads |
| `logger/writer/` | `Factory` / `Name` / `Config` and the process-wide registry mapping a writer name to a `Sink`-producing factory, each SDK writer's configuration value and the network writers' credential port (ADR 0012) | `0.2.3.*` | `internal/service/observe/logger/writer/*` |
| `logger/writer/{journald, nettransport, rotfile}/` | the codes and sentinels of the three writers that declare any — codes only (ADR 0160 §2) | `0.3.31.*`, `0.3.30.*`, `0.3.27.*` | `internal/service/observe/logger/writer/{journald,nettransport,rotfile}` |
| `metrics/` | the OpenTelemetry metrics data model: the instruments, the frozen `Meter` and its siblings, `Temporality`, the `Exporter` registry and `SnapshotValue` (ADR 0027, ADR 0044) | `0.2.9.*` | `internal/service/observe/metrics` |
| `otel/` | the types every signal shares — `AttrValue`, `ResourceValue`, `ScopeValue` — and no code: its guards panic with the sentinel their caller passes (ADR 0051 §Decision 2) | none | none — `metrics/` and `trace/` build on it |
| `trace/` | the frozen `Tracer` and `Span` ports, `SpanContextValue`, the W3C Trace Context format, the span model, the `Sampler` and `SpanSink` FUNC ports, `Carrier` and the `SpanExporter` registry (ADR 0051) | `0.2.20.*` | `internal/service/observe/trace` |

Three edges inside the family are decisions and are kept: `metrics` and `trace`
both import `otel` and neither imports the other; `logger/writer` imports
`logger` and `logger/level`; `logger` imports `logger/level` and no signal —
its bridge to `trace` is `pkg/v1/observe/logger`'s, the one place the two
domains meet (ADR 0062).

A code keeps its value when its package moves (ADR 0160): the `0.2.*` ranges
above are the ones these packages declared before the family existed, the
`0.3.*` ones are the engines', declared here since ADR 0160 §2 at each
engine's path, and `codeRangeOwners` names the new directories under the same
keys. The code mirrors beneath `logger/` import `kernel/errs` and nothing of
the family.

## Do NOT

- Put Go code in this directory. A file here would make `observe` a package
  of its own, and the family a domain nobody chose.
- Import `trace` or `metrics` from `logger`: every consumer who wants a line on
  stderr would link a signal it never asked for (ADR 0062).
- Twin `otel`'s model inside one signal. What both signals emit identically is
  `otel`'s; what only one emits is that signal's.
- Renumber a member's codes to match its new path. Nothing derives a code from
  a directory, and a consumer branches on the value (ADR 0160).
