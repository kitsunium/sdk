# metrics

Package `metrics` declares the SDK observability port: `Counter`/`Gauge`/
`Histogram` instruments, the `Meter` (mint + `Collect`), the `LabelValue` that
gives a series its dimensions, and the `Exporter` contract + registry. One
instrument name plus one label set is one series; a `SnapshotValue` maps each
name to its series. The in-memory meter + a text exporter live in
`internal/service/metrics`; facade: `pkg/v1/metrics`. ADR 0027. See `CLAUDE.md`.
