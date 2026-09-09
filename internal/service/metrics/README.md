# metrics (service)

In-memory `Meter` + lock-free Counter/Gauge/Histogram instruments implementing
`internal/core/metrics`, plus a stdlib text Exporter. Instruments are keyed by
name and label set, with a per-name cardinality bound whose excess folds into a
visible aggregated overflow series; resolving a series allocates nothing (see
`BENCH.md`). Cross-OS portable. Public facade: `pkg/v1/metrics`. ADR 0027. See
`CLAUDE.md`.
