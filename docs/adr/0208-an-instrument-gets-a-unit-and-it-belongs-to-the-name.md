# ADR 0208 — an instrument gets a unit, and it belongs to the NAME

- **Status**: Accepted
- **Date**: 2026-11-03
- **Deciders**: SDK maintainers
- **Related**: [ADR 0067](0067-metrics-instrument-description.md) (the description, and the deferral this ADR resolves), [ADR 0044](0044-metrics-adopts-the-otel-data-model.md) (the data model), [ADR 0048](0048-sdk-metrics-otlp-json.md) (the OTLP/JSON wire), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a sibling port), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (the v0 licence), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (refuse rather than accept an inert call)

## Context

ADR 0067 brought `Metric.description` into the model and deferred `Metric.unit`
by name: "It needs its own ADR", because on the Prometheus wire a unit is a
name suffix (`_seconds`, `_bytes`) under the interoperability rules, and this
connector refuses every non-injective rename of a name. Meanwhile OTLP's
`Metric.unit` (field 3, UCUM: `s`, `ms`, `By`, `{request}`) is what Grafana,
Honeycomb and the collector's unit-aware processors read to label an axis and
to convert, and the OpenTelemetry semantic conventions give every instrument
they define a unit — the Go runtime instruments (#868) among them. A latency
histogram in seconds and one in milliseconds were indistinguishable on every
wire (#1021).

## Decision

1. **A unit belongs to the instrument NAME, as a description does.** A sibling
   port `UnitDescriber` — `Unit(name, unit string)` — beside `Describer`, found
   by type assertion and NOT folded into `FullMeter` (ADR 0039: the absence of
   the sibling is the answer). It is wiring-time: nothing on the observation
   path reads it, and the meter's map is nil until the first `Unit`.
2. **Two refusals, both panics, as Describe's.** A unit that is empty, longer
   than 63 bytes or holds a byte outside printable ASCII `0x21`–`0x7E` — UCUM's
   case-sensitive grammar has no space and no control byte — panics with
   `InvalidUnit` (`0.2.9.9`); a second, DIFFERENT unit for a bound name panics
   with `UnitConflict` (`0.2.9.10`). The same unit again is idempotent. The SDK
   checks the alphabet and the length, not UCUM's full grammar: a unit is
   prose a backend reads, and a parser here would be a second, partial UCUM.
3. **The model carries it.** `SumMetricValue`, `GaugeMetricValue` and
   `HistogramMetricValue` gain `Unit string` beside `Description`, under the v0
   licence (ADR 0040) — an added field, no existing reader changes.
4. **The wires.**
   - **OTLP/JSON**: `Metric.unit` emitted when non-empty, after `description`
     (proto field order), the presence rule of `description`: a meter with no
     unit writes the document byte for byte as before.
   - **Text exporter**: ` unit="…"` after the description on the `# metric`
     header, quoted like it.
   - **Prometheus**: NOTHING. The connector adds no `_seconds` suffix and no
     `# UNIT` line (the latter is OpenMetrics, which this connector does not
     write): the unit is lost on that wire, and the loss is pinned by a test
     that holds the exposition byte-identical with and without a unit. An
     opt-in suffix is left to a later decision — it renames series.

## Consequences

- The runtime instruments (#868) and the Stats bridge (#871) declare their
  units through the same port.
- Two codes, `0.2.9.9` and `0.2.9.10`, in the `0.2.9.*` block the core metrics
  package already owns: no new range, no ownership row.
- Out of scope: `Metric.metadata`, per-point units, a unit-aware conversion in
  the SDK.

## Why not

- **A unit argument on every instrument constructor.** It would widen
  `Meter`, which ADR 0039 forbids, and make the unit an identity of the series
  — two fetches of `latency` with different units would be two series of one
  name, which no backend can reconcile.
- **"First wins, quietly".** The OpenTelemetry SDK keeps the first and logs a
  warning; this SDK has no logger in the metrics core and refuses instead, for
  the reason ADR 0067 gives for descriptions.
