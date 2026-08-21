# metrics

Package `metrics` declares the SDK observability port: `Counter`/`Gauge`/
`Histogram` instruments, the `Meter` (mint + `Collect`), and the `Exporter`
contract + registry. The in-memory meter + a text exporter live in
`internal/service/metrics`; facade: `pkg/v1/metrics`. ADR 0027. See `CLAUDE.md`.
