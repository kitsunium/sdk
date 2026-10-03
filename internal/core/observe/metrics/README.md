# metrics

Package `metrics` declares the SDK observability port, shaped on the
OpenTelemetry metrics **data model** and importing none of its code:
`Counter`/`UpDownCounter`/`Gauge`/`Histogram` (plus their observable
counterparts), the `Meter` (mint + `Collect`), aggregation `Temporality`, the
`Describer` sibling that documents an instrument NAME, and the `Exporter`
contract + registry. The typed attribute that gives a series its dimensions,
the producing Resource and the Scope that instrumented it are
`internal/core/observe/otel`'s — shared with the trace signal — while the
`InvalidAttribute` refusal (`0.2.9.4`), the `DefaultScopeName` and the
`OverflowAttrKey` are this signal's own. One instrument name plus one attribute
set is one series; a `SnapshotValue` maps each name to its metric and each
metric to its series. The in-memory meter + the text, Prometheus and OTLP
exporters live in `internal/service/observe/metrics`; facade: `pkg/v1/observe/metrics`.
ADR 0027, ADR 0044, ADR 0067. See `CLAUDE.md`.
