# trace

Package `trace` declares the SDK's distributed-tracing port, shaped on the
OpenTelemetry trace **data model** and importing none of its code: `Tracer`
(one method, frozen) and `Span` (five, frozen), the immutable
`SpanContextValue` that travels between processes, the W3C **Trace Context**
`traceparent`/`tracestate` format implemented from the ABNF, `SpanValue` with
its `SpanKind` / `StatusValue` / `EventValue` / `LinkValue`, the `Sampler` and
`SpanSink` func ports, the two-method `Carrier` (which `http.Header` satisfies
with no adapter), and the `SpanExporter` contract + registry. The attribute,
`ResourceValue` and `ScopeValue` types are **aliases** of `internal/core/metrics`'
— they are `common.proto`/`resource.proto`, shared by every signal, so
`trace.Attr` and `metrics.Attr` are one type. The sampling decision is taken
ONCE, at the root, and travels in the `sampled` bit. Tracer, samplers, recorder
and the OTLP/JSON encoder live in `internal/service/trace`; facade:
`pkg/v1/trace`. ADR 0051. See `CLAUDE.md`.
