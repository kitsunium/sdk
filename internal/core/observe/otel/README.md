# otel

Package `otel` holds the OpenTelemetry types every signal shares — the typed
`AttrValue` (`common/v1` `KeyValue`/`AnyValue`), the producer's `ResourceValue`
(`resource/v1`) and the instrumentation's `ScopeValue` (`common/v1`) — so
`internal/core/observe/metrics` and `internal/core/observe/trace` both build on one model
instead of trace borrowing metrics'. It declares no error code: `ValidateAttrs`,
`SortAttrs` and `NormalizeResource` panic with the sentinel their CALLER passes,
so each signal names an unusable attribute set with its own code, and
`NormalizeScope` takes the signal's own default name. Facades: `pkg/v1/observe/metrics`
and `pkg/v1/observe/trace` alias these types, so `metrics.Attr` and `trace.Attr` are one
type. ADR 0044, ADR 0051 §Decision 2. See `CLAUDE.md`.
