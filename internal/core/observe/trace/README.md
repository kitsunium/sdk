# trace

Package `trace` declares the SDK's distributed-tracing port, shaped on the
OpenTelemetry trace **data model** and importing none of its code: `Tracer`
(one method, frozen) and `Span` (five, frozen), the immutable
`SpanContextValue` that travels between processes, the values the W3C **Trace
Context** headers carry (the identifiers, the flags, the ordered `tracestate`
list and the `StateBuilder` that fills it under the grammar) — reading and
writing the headers is the engine's since ADR 0160 §4, `SpanValue` with
its `SpanKind` / `StatusValue` / `EventValue` / `LinkValue`, the `Sampler` and
`SpanSink` func ports, the two-method `Carrier` (which `http.Header` satisfies
with no adapter), and the `SpanExporter` contract + registry. The attribute,
`ResourceValue` and `ScopeValue` types are `internal/core/observe/otel`'s — they are
`common.proto`/`resource.proto`, shared by every signal, so `trace.Attr` and
`metrics.Attr` are one type — while the `InvalidAttribute` refusal an unusable
set earns here (`0.2.20.7`) and the `DefaultScopeName` are this signal's own.
The sampling decision is taken
ONCE, at the root, and travels in the `sampled` bit. Tracer, samplers, recorder,
the W3C parsers with `Inject` / `Extract`, and the OTLP/JSON encoder live in
`internal/service/observe/trace`; facade:
`pkg/v1/observe/trace`. ADR 0051. See `CLAUDE.md`.

The ports — `Tracer`, `Span`, `Carrier` and `SpanExporter` — are generated from
`design/observe/trace.yaml` into `design_gen.go` (ADR 0163): a port changes in
the design, then `kit gen`.
