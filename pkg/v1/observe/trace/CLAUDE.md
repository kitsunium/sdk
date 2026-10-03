<!-- updated: 2026-10-02T20:02:20Z -->
# pkg/v1/observe/trace/

## Purpose

The public facade for the SDK's **distributed-tracing** domain — the third pillar
of observability beside `pkg/v1/observe/logger` and `pkg/v1/observe/metrics`. Type aliases onto
`internal/core/observe/trace` and `internal/service/observe/trace`, plus thin forwarding
functions. Admitted by **ADR 0051**.

It speaks the OpenTelemetry trace data model and the W3C Trace Context
propagation format, and imports nothing from `go.opentelemetry.io`.

## Surface

One production file, `trace.go`, for the reason `pkg/v1/observe/metrics` has one: rule 10
generates `README.md` from the package doc comment, and a second file carrying a
`// Package trace` comment makes which doc gomarkdoc renders a matter of
alphabetical order.

| Group | Names |
|---|---|
| Ports | `Tracer`, `Span`, `Sampler`, `SpanSink`, `Carrier`, `SpanExporter`, `ExporterName` |
| Model | `TraceID`, `SpanID`, `TraceFlags`, `SpanContext`, `TraceState`, `Kind`, `Status`, `StatusCode`, `Event`, `Link`, `SpanData`, `Spans`, `Attr`, `AttrKind`, `Resource`, `Scope`, `SpanParams`, `SamplingParams` |
| Config | `TracerConfig`, `RecorderConfig`, `OTLPHTTPConfig`, `Recorder`, `SDKTracer` |
| Construction | `NewTracer`, `NewRecorder`, `NewTraceID`, `NewSpanID` |
| Sampling | `AlwaysSample`, `NeverSample`, `ParentBased`, `Ratio` |
| Propagation | `Inject`, `Extract`, `ParseTraceParent`, `FormatTraceParent`, `ParseTraceState`, `ParseTraceID`, `ParseSpanID`, `ContextWithSpanContext`, `SpanContextFromContext` |
| Instrumentation | `RecordError`, `ServerMiddleware`, `ClientMiddleware` |
| Export | `EncodeOTLPJSON`, `NewOTLPJSONExporter`, `NewOTLPHTTPExporter`, `OTLPRetryable`, `RegisterExporter`, `LookupExporter`, `AvailableExporters`, `Export` |
| Attributes | `String`, `Bool`, `Int64`, `Float64` |
| Constants | `Kind*`, `Status*`, `AttrKind*`, `FlagSampled`; the W3C names and bounds `TraceParentHeader`, `TraceStateHeader`, `TraceParentLen`, `VersionSupported`, `MaxTraceStateMembers`; the attribute keys `ServiceNameKey`, `ExceptionEventName`, `ExceptionTypeKey`, `ExceptionMessageKey`, `HTTPRequestMethodKey`, `HTTPResponseStatusCodeKey`, `URLPathKey`, `URLSchemeKey`, `URLFullKey`, `ServerAddressKey`; `DefaultScopeName`, `DefaultMaxSpans`, `OTLPTracesPath`, `DefaultOTLPTimeout`, `DefaultOTLPMaxResponseBytes` |
| Sentinels | core: `InvalidTraceParent`, `InvalidTraceState`, `InvalidSpanName`, `InvalidAttribute`, `UnknownExporter`, `ExportFailed`, `DuplicateRegistration`; service: `EntropyFailed`, `InvalidSampleRatio`, `OTLPInvalidSpanContext`, `OTLPSpanNotEnded`, `OTLPEndpointInvalid`, `OTLPExportRejected`, `OTLPExportUnavailable`, `OTLPPartialSuccess` |

## `trace.Attr` IS `metrics.Attr`

Not "compatible with" — the same type. Both alias
`internal/core/observe/otel.AttrValue`, which is OTel's `common/v1.KeyValue`/`AnyValue`,
shared by every signal. So is `trace.Resource`, which matters more: `service.name`
is the key a backend correlates a trace with a metric on, and two Resource types
could disagree about it. (`ServiceNameKey` is one constant for the same reason.)

`TestPublicAttributeIsTheMetricsAttribute` pins the assignment in both
directions. ADR 0051 §Decision 2 has the reasoning; the extraction it deferred —
the shared model in its own package, under both signals — is done, so this
package no longer reaches the metrics port at all.

What is NOT shared is the refusal: an unusable attribute set on a span, event,
link or Resource panics with this package's `InvalidAttribute` (`0.2.20.7`),
where it used to carry the metrics code (`0.2.9.4`) because the refusal
travelled with the type. And `Resource` / `Scope` no longer carry a
`Normalized()` method — the tracer normalises them itself, with this signal's
code and this signal's `DefaultScopeName` (the old `Scope.Normalized()` stamped
the METRICS package's name). A v0 published-shape change (ADR 0040).

## `SDKTracer` is what `NewTracer` returns, and `Tracer` stays the port

`NewTracer` returns the concrete `*svctrace.Tracer`, not the `Tracer` port, so a
caller keeps `Resource` and `Scope`, and whatever the concrete type grows later,
without widening a frozen port (ADR 0039). Until #259 no alias published that
type: a consumer could call `NewTracer` and use the result, but could not write
its type down, so a tracer could not sit in a field of the consumer's own. The
alias cannot take the name `Tracer`, which the port has held since ADR 0051 and
which renaming would break; `SDKTracer` follows OpenTelemetry's split between
the API, which is the port, and the SDK, which implements it.
`TestAConsumerCanNameTheTracerNewTracerReturns` keeps one in a struct field and
still hands it to the port, from the `_test` package alone.

## What a consumer needs to know first

1. **Sampling is decided once, at the root.** `Ratio` REFUSES a fraction of
   exactly 0, because that value also spells "nobody configured this" — say
   `NeverSample`, which cannot be produced by forgetting anything.
2. **`Extract` never fails a request.** A malformed `traceparent` starts a new
   trace, which is what W3C §4.3 requires. `ParseTraceParent` is the typed-error
   form, for diagnosis.
3. **The middlewares are the network domain's own type**, so they compose with
   `net.Chain`. The client one goes on the transport, where no call site can skip
   it.
4. **`NewOTLPHTTPExporter` is not registered and does not retry.** Construct it
   explicitly with a full URL (`endpoint + trace.OTLPTracesPath`), and hand
   `trace.OTLPRetryable` to `pkg/v1/app/resilience` if you want a backoff you can see.

## Do NOT

- **Do NOT hand-edit `README.md`.** It is generated by `gomarkdoc` from the
  package doc comment (rule 10, ADR 0008). Edit the comment in `trace.go` and run
  `make docs-readme`; `scripts/pre-commit/check-readme-drift.sh` (pre-commit
  hook and CI's `bazel` job) fails on drift.
- **Do NOT add a second production `.go` file with a package doc comment.** See
  §Surface.
- **Do NOT widen `Tracer`, `Span` or `Carrier`.** They are published aliases of
  frozen ports (ADR 0039); a sixth `Span` method breaks every downstream double
  at compile time.
- **Do NOT re-export a concrete service type as a named type.** The aliases are
  deliberate: `Recorder = svctrace.Recorder` keeps one type, so a value built by
  either path is the same value.

## Verification

| Command | Expected |
|---|---|
| `cd pkg && GOWORK=off go test ./v1/observe/trace/` | green |
| `bazel test //pkg/v1/observe/trace:trace_test` | green |
| `make docs-readme` | `README.md` unchanged |
| `TestPublicAttributeIsTheMetricsAttribute` | `trace.Attr` and `metrics.Attr` are one type |
| `TestFacadeRefusesTheAmbiguousRatio` | the ADR 0031 answer, at the public edge |
| `TestAConsumerCanNameTheTracerNewTracerReturns` | what `NewTracer` returns has a public name, and is still the port |
