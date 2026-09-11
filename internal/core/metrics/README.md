# metrics

Package `metrics` declares the SDK observability port, shaped on the
OpenTelemetry metrics **data model** and importing none of its code:
`Counter`/`UpDownCounter`/`Gauge`/`Histogram` (plus their observable
counterparts), the `Meter` (mint + `Collect`), the typed `AttrValue` that gives
a series its dimensions, aggregation `Temporality`, the producing
`ResourceValue` and the `ScopeValue` that instrumented it, and the `Exporter`
contract + registry. One instrument name plus one attribute set is one series; a
`SnapshotValue` maps each name to its metric and each metric to its series. The
in-memory meter + the text and Prometheus exporters live in
`internal/service/metrics`; facade: `pkg/v1/metrics`. ADR 0027, ADR 0044. See
`CLAUDE.md`.
