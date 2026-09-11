# metrics (service)

In-memory `Meter` + lock-free Counter/UpDownCounter/Gauge/Histogram instruments
and their observable counterparts, implementing `internal/core/metrics`, plus
three stdlib Exporters: a lossless **text** diagnostic, a deliberately lossy
**Prometheus** connector, and the native **OTLP/JSON** wire — an encoder
(`EncodeOTLPJSON`, snapshot to bytes, no I/O) plus an OTLP/HTTP emitter, written
from the specification and importing neither `go.opentelemetry.io` nor a
protobuf runtime. Instruments are keyed by name and TYPED attribute set,
with a per-name cardinality bound whose excess folds into a visible aggregated
overflow series; resolving a series allocates nothing (see `BENCH.md`). Under
delta temporality `Collect` consumes the window it reports. Cross-OS portable.
Public facade: `pkg/v1/metrics`. ADR 0027, ADR 0044, ADR 0048. See `CLAUDE.md`.
