# trace (service)

The concrete `Tracer` implementing `internal/core/trace`, plus the four
samplers (`AlwaysSample` / `NeverSample` / `ParentBased` / `Ratio` — a
deterministic fraction of the trace id, refusing an ambiguous rate of 0), the
in-memory `Recorder` whose overflow is counted rather than silent, the
`RecordError` helper, and the native **OTLP/JSON** wire on `/v1/traces`: an
encoder (`EncodeOTLPJSON`, batch to bytes, no I/O) plus an OTLP/HTTP emitter,
written from the specification and importing neither `go.opentelemetry.io` nor a
protobuf runtime. Trace and span identifiers ride as **hex**, not base64 — the
one place OTLP overrides the protobuf-JSON mapping. `ServerMiddleware` and
`ClientMiddleware` are `internal/core/net`'s own generic middleware type,
instantiated at `http.Handler` and `http.RoundTripper`. The OTLP/HTTP emitter is
deliberately **not** registered. Cross-OS portable. Public facade:
`pkg/v1/trace`. ADR 0051. See `CLAUDE.md`.
