# ADR 0027 — Observability domain (`metrics`)

- **Status**: Accepted. **The port's SHAPE is amended by ADR 0044** — the domain now speaks the OpenTelemetry metrics data model (typed attributes, aggregation temporality, Resource, InstrumentationScope, non-monotonic sums, observable instruments), implemented from the specification with zero `go.opentelemetry.io` imports; `SnapshotValue` gained a per-name metric envelope, and the Prometheus exporter was requalified as a deliberately lossy connector. **Exporter destination amended by ADR 0030** — the registered `text` exporter writes to stderr, not stdout. **Labels are no longer deferred**: instruments are keyed by name + label set, with a per-name cardinality bound and an aggregated overflow series (see §Deferred). **The Prometheus text-exposition exporter is no longer deferred either** — it landed in-tree and stdlib-only rather than as a third-party package, because the text format needs no client library (see §Deferred). OTLP, the Prometheus protobuf format and the push loop remain deferred.
- **Date**: 2026-06-24
- **Deciders**: SDK maintainers
- **Related**: ADR 0024 (Phase-B wave), ADR 0012 (writer registry — the exporter-registry model), ADR 0011 (snapshot), ADR 0005/0006 (codes)
- **Amends**: covered by the ADR 0024 Phase-B purpose-statement widening

## Context

The SDK ships a structured logger but no **metrics** — counters/gauges/
histograms + an export path. Metrics are the logger's natural twin and reuse the
same kernel substrate (atomics, the writer-registry export model). Phase B adds
the `metrics` domain.

## Decision

1. **Add `internal/core/metrics`** — instruments (`Counter`/`Gauge`/`Histogram`),
   the `Meter` (mints instruments + `Collect() SnapshotValue`), and the
   `Exporter` contract + process-wide registry (the **writer-registry** model,
   ADR 0012). Instruments are multi-method domain interfaces; `Collect` lives on
   `Meter` (no separate Collector).
2. **`internal/service/metrics`** — an in-memory `Meter` with **lock-free**
   instruments (atomic counter; CAS float gauge; bucketed histogram with atomic
   counts/sum) and a stdlib **text** Exporter (registered to stdout on import).
3. **`pkg/v1/metrics`** — aliases + `NewMeter`/`Export`/`RegisterExporter`/
   `NewTextExporter`/`AvailableExporters`.
4. **Error block `0.2.9.*`**: `UNKNOWN_EXPORTER`, `EXPORT_FAILED`,
   `INSTRUMENT_KIND_CONFLICT`, `DUPLICATE_REGISTRATION`.

### Cross-platform (ADR 0018)

100 % portable Go (sync/atomic/math). No OS-specific code.

## Consequences

- 9th core sibling (with registry, like writer). `pkg/v1` gains a dep-light
  facade. Docs + `error-codes.yaml` updated per rule 11.

## Why not

- **OpenTelemetry SDK wrapper** — rejected for v1: heavy dep; the in-house
  instruments + exporter registry keep the core dep-light, and an OTLP exporter
  can land later as a **third-party** quarantined package (deferred).
- **Labels in v1** — deferred at the time, and **since shipped in place**: a
  labelled instrument needs a label-set map key + cardinality control, and both
  now exist. One instrument name plus one label set is one series; the key is
  the sorted, length-prefixed encoding of both; a per-NAME bound
  (`MaxSeriesPerInstrument`, default 2000) folds the excess into one aggregated
  series carrying `sdk_metric_overflow="true"`. A non-positive bound clamps to
  the default and never means unbounded (ADR 0031). `SnapshotValue` changed
  shape from `map[name]value` to `map[name][]seriesValue` to carry the
  dimensions — a minor bump under ADR 0007 §Bump semantics, with no prior
  published surface to break. Impl + rationale:
  `internal/service/metrics/CLAUDE.md` §Cardinality policy.
- **Separate `Collector` interface** — rejected: putting `Collect` on `Meter`
  avoids a single-method interface and a second type.

## Breaking changes

None. `metrics` is a new domain in this change set — there is no prior
published surface to break.

One contract was tightened during review, before any release: the text
`Exporter` now serialises its single `dst.Write` behind a mutex.
`core/metrics.Exporter` documents that implementations MUST be safe for
concurrent use, and `NewTextExporter` accepts an arbitrary `io.Writer` —
`os.Stdout` tolerates concurrent writes on most platforms, but a
`bytes.Buffer` does not, and `-race` reports the data race. Rendering stays
outside the lock so a slow writer serialises callers without also serialising
the formatting work.

## Deferred

- ~~Labelled dimensions (label sets + cardinality limits)~~ — shipped, see
  §Why not above.
- ~~Prometheus + OTLP exporters as **third-party** quarantined packages (heavy
  deps), self-registering via the exporter registry.~~ — **split, and the
  Prometheus half shipped in-tree**. The premise "heavy deps" holds for OTLP and
  for the Prometheus *protobuf* format; it does not hold for the Prometheus
  **text exposition format**, which is a line-oriented format an exporter writes
  with `strconv` alone. Quarantining it would have bought a dependency on
  `client_golang` in exchange for nothing, against the SDK's dep-light rule, so
  `internal/service/metrics` renders it directly and self-registers as
  `prometheus` (stderr, ADR 0030) beside `text`. It needed a per-package error
  block — `0.3.45.*` — because it REFUSES an instrument or label name the
  exposition grammar cannot spell instead of transliterating it, which would not
  be injective and would merge distinct instruments into one family. Rationale +
  format citations: `internal/service/metrics/CLAUDE.md` §The Prometheus text
  exposition format. OTLP and the protobuf format remain deferred as written.
- Active push/scrape loop (`worker.Every`) — v1 is pull-on-demand (`Collect`+`Export`).

## References

- Impl: `internal/core/metrics/`, `internal/service/metrics/`, `pkg/v1/metrics/`.
- ADR 0012 (writer registry), ADR 0024 (Phase-B wave), ADR 0018 (portability).
