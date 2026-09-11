# internal/service/trace/

## Purpose

The concrete tracing implementation behind `internal/core/trace`: a `Tracer`, the
live span, four samplers, an in-memory `Recorder`, the `RecordError` helper, the
**OTLP/JSON** encoder and OTLP/HTTP emitter for `/v1/traces`, and the two HTTP
middlewares. Admitted by **ADR 0051**.

Everything here is written from the OpenTelemetry and W3C specifications with the
standard library. Nothing imports `go.opentelemetry.io`; nothing imports a
protobuf runtime.

Code range: `0.3.50.*` (ADR 0051).

## Contents

| File | Surface |
|---|---|
| `tracer.go` | package doc + `Tracer` + `NewTracer` + `Start` (the mint/sample/inherit sequence) |
| `config.go` | `TracerConfig` + every clamp, applied once in `resolved()` |
| `span.go` | the recording `span`: mutex-guarded attrs/events/status, idempotent `End` |
| `noop_span.go` | the span an unsampled trace gets — and why it still carries a context |
| `sampler.go` | `AlwaysSample` / `NeverSample` / `ParentBased` / `Ratio` |
| `idgen.go` | `NewTraceID` / `NewSpanID` + `readRandom` |
| `recorder.go` | `Recorder` + `RecorderConfig` + `Sink` / `Collect` / `Len` / `Dropped` |
| `record_error.go` | `RecordError` — the conventional `exception` event |
| `otlp_request.go` | the Go mirror of `trace.proto` / `common.proto` / `resource.proto`, in FIELD-NUMBER order |
| `exporter_otlpjson.go` | `EncodeOTLPJSON` + the writer-bound exporter + `OTLPJSON` (registered, stderr) |
| `exporter_otlphttp.go` | `NewOTLPHTTPExporter` + `OTLPRetryable` + `OTLPTracesPath` — the only file with an outbound socket |
| `otlphttp_config.go` | `OTLPHTTPConfig` |
| `otlp_export_response.go` | the partial-success body + the lenient 64-bit decoder |
| `http_server.go` | `ServerMiddleware` + the semantic-convention keys + `statusRecorder` |
| `http_client.go` | `ClientMiddleware` + `tracedRoundTripper` |
| `numeric.go` | the strconv parameters shared by the encoder and the samplers |
| `codes.go` | `Code*` constants — range 0.3.50.* |
| `errors.go` | `EntropyFailed` / `InvalidSampleRatio` / `OTLPInvalidSpanContext` / `OTLPSpanNotEnded` / `OTLPEndpointInvalid` / `OTLPExportRejected` / `OTLPExportUnavailable` / `OTLPPartialSuccess` |

## `Ratio(0)` is refused — the ADR 0031 answer

Does a sampling rate of `0` mean "sample nothing" or "nobody configured this"?

It means **both**, and nothing in a `float64` can tell them apart: an unset struct
field, a JSON document missing the key and a `rate:` with nothing after it all
produce `0.0`. A sampler that honoured the number would disable tracing for a
deployment that believed it was configured, and the symptom is the **absence of
telemetry** — no error, no log line, nothing to alert on, because "no traces" and
"a quiet service" look identical.

So the ambiguity is refused rather than resolved. `NeverSample` is the name for
none and it cannot be produced by forgetting anything; `AlwaysSample` (or
`Ratio(1)`) is the name for all. NaN, negatives and anything above 1 are refused
for the ordinary reason.

Everything else here **clamps**, and the difference is the ADR 0031 test applied
honestly — clamp where the SDK is not substituting judgement:

| Unset | Clamps to | Why |
|---|---|---|
| `TracerConfig.Sampler` | `ParentBased(AlwaysSample)` | the SAFE direction; dropping silently would make a misconfiguration look like a quiet service |
| `TracerConfig.Clock` | `clock.System` | "the real clock" is a description |
| `TracerConfig.Sink` | a named discard | a tracer with no destination has nowhere to put a span; buffering into a slice nobody drains is the leak that shape invites |
| `RecorderConfig.MaxSpans` | 2048 | there is NO unbounded setting |
| `SpanParams.StartTime` | now | same as the clock |
| `ParentBased(nil)` | `AlwaysSample` | the one clamp in the sampler family, for the safe-direction reason |

## What the OTLP/JSON encoder does that a generated one would not

ADR 0048's four protobuf-JSON rules apply unchanged. This signal adds a fifth and
a sixth, and both are silent when wrong:

1. **`traceId` and `spanId` are HEX, not base64.** The OTLP specification
   overrides the standard mapping by name for exactly these two fields. A base64
   identifier is 24 characters of plausible text that no collector accepts, so
   nothing surfaces unless a test asserts the alphabet — `TestEncodeOTLPJSONIdentifiersAreHexNotBase64`
   asserts both the hex form's presence and the base64 form's absence.
2. **Not every integer is 64-bit.** `flags` is `fixed32`, so it rides as a plain
   JSON **number**. Widening it to a decimal string is exactly as wrong as
   narrowing a timestamp to a number.

Presence decisions:

- **`flags` is always emitted.** It has no presence and would legitimately be
  omitted at zero, but it carries the sampled bit and whether the parent was
  REMOTE — a field that vanishes precisely when it carries the surprising answer
  is one a reader cannot trust. It is never actually zero here, because
  `SPAN_FLAGS_CONTEXT_HAS_IS_REMOTE_MASK` is always set: without that bit a
  receiver cannot tell "the parent is local" from "this producer does not track
  it", and this producer does.
- **`status` is OMITTED when unset** — the opposite call from the metrics
  encoder's three always-emitted fields, and the difference is presence.
  `asInt`, the histogram `sum` and `isMonotonic` each carry information their
  absence destroys; `STATUS_CODE_UNSET` is the schema's default and an
  all-default `Status` carries exactly what its absence carries.
- **`parentSpanId` is omitted on a root.** The schema's spelling for "no parent"
  is an absent field, not sixteen zero digits — which would be the INVALID
  identifier W3C declares.

Two refusals, both of STRUCTURE, so each fails on the first export or never:

| Refused | Sentinel | Why |
|---|---|---|
| an all-zero trace-id or span-id | `OTLPInvalidSpanContext` `0.3.50.3` | the schema requires 16 and 8 real bytes; W3C declares the all-zero form invalid |
| a span with no `EndTime` | `OTLPSpanNotEnded` `0.3.50.4` | `end_time_unix_nano` is required; a zero claims the Unix epoch and renders as a 56-year span |

**The conformance test compares against bytes written BY HAND** from the `.proto`
files, with the field numbers in comments beside each fragment. A test that
decoded the encoder's own output would prove self-consistency — exactly the
property a wrong field name or a base64 identifier preserves.

## The OTLP/HTTP emitter is NOT registered

`otlpjson` self-registers on **stderr** (ADR 0030). `NewOTLPHTTPExporter` is
registered nowhere, and the reason is one step beyond ADR 0030 rather than a
matter of degree: **there is no endpoint that could be a correct default**, so a
registered emitter would POST at whatever answers on an address the caller never
named — inside a cluster, a real host belonging to somebody else.
`TestOTLPHTTPExporterIsNeverRegistered` pins the absence.

It is a connector, written like `writer/nettransport`: bounded response read, no
redirects (CWE-918 — an unfollowed `30x` classifies as the permanent rejection a
misconfigured endpoint is), the endpoint refused at construction unless it is an
absolute `http(s)` URL with a non-root path, and **no retry**. It classifies in
exactly `resilience.RetryConfig.Retryable`'s shape:

```go
resilience.NewRetry(resilience.RetryConfig{
    MaxAttempts: 3,
    BaseDelay:   time.Second,
    Retryable:   trace.OTLPRetryable,
})
```

The retryable set is spelled out — 429/502/503/504 and a transport fault — because
**5xx is not retryable as a class**: a 500 or a 501 means the same request will
fail the same way. An **empty batch is a no-op**, not a POST.

## The middlewares

Both are `internal/core/net`'s OWN generic middleware type, instantiated:
`corenet.Middleware[http.Handler]` and `corenet.Middleware[http.RoundTripper]`.
They compose with `corenet.Chain` rather than beside it.

They decorate the HTTP types rather than the group's `ConnHandler` because a
traceparent is an HTTP **header** — the connection middleware sees bytes. The
client one sits at the transport for the reason ADR 0029 puts the Policy there:
no call site can skip it.

Four decisions:

- **The server span is named by the METHOD alone.** A path carries identifiers, so
  `GET /users/42` and `GET /users/43` are two operations to a backend and one to a
  human. The conventions ask for `{method} {route}` where route is the *template*;
  this SDK does not route, so it has none and does not invent one. The path is an
  ATTRIBUTE, in its **escaped** form — `url.URL.Path` is already percent-decoded,
  so recording it would report `%2e%2e` as `..`.
- **4xx is an error on a CLIENT span and not on a SERVER span.** `httpClientErrorFloor`
  is 400, `httpServerErrorFloor` is 500. It is the OTel conventions' own asymmetry
  and it is the constant a reader is most likely to assume is a copy-paste slip: a
  404 is the caller asking for something that is not there, and marking it ERROR
  would make every scanner probing for `/wp-admin` light up a service's error rate.
- **`statusRecorder` implements `Unwrap` and NOTHING ELSE.** Declaring `Flush` and
  `Hijack` that forward would make the wrapper claim both capabilities
  UNCONDITIONALLY, so `w.(http.Hijacker)` would succeed on a writer that cannot
  hijack and the failure would surface inside a protocol upgrade. **That is the
  exact defect ADR 0047 fixed in the listener engine, re-introduced by a
  middleware.** `http.ResponseController` walks the `Unwrap` chain and asks the
  real writer — which is how this SDK's own SSE and WebSocket implementations
  already find `Flush` and `Hijack`. Both directions are tested.
- **The client middleware CLONES the request.** `http.RoundTripper`'s contract
  says an implementation "should not modify the request", and net/http retries an
  idempotent request on a fresh connection using the same `*http.Request`.

## Do NOT

- **Do NOT consult the sampler below the root.** It produces traces with holes,
  and the holes are invisible at the process that makes them.
- **Do NOT return nil from `Start`.** An unsampled span must still carry a
  context, or the decision stops propagating and one dropped trace becomes N kept
  ones downstream. It would also put `if span != nil` at every instrumentation
  site.
- **Do NOT accept `Ratio(0)`.** See above.
- **Do NOT register `NewOTLPHTTPExporter`, or give it a default endpoint.**
- **Do NOT retry inside `Export`.** `resilience` owns backoff; a hidden one cannot
  be tuned or cancelled, and `Export` has no context to cancel it with.
- **Do NOT forward `Flush`/`Hijack` from `statusRecorder`.** See above.
- **Do NOT decode the encoder's own output in a conformance test.**
- **Do NOT put the path in a span name.**

## Verification

| Command | Expected |
|---|---|
| `GOWORK=off go test ./trace/...` (from `internal/service`) | green |
| `bazel test //internal/service/trace:trace_test` | green |
| `TestEncodeOTLPJSONMatchesTheHandWrittenSchemaDocument` | byte-for-byte against the hand-written `.proto` document |
| `TestEncodeOTLPJSONIdentifiersAreHexNotBase64` | the fifth protobuf-JSON rule |
| `TestSamplerIsConsultedOnceAtTheRoot` | exactly one call across a three-level trace |
| `TestAnUnsampledTraceStillPropagates` | zero spans recorded, a valid unsampled context propagated |
| `TestRatioRefusesZeroBecauseZeroIsAmbiguous` | the ADR 0031 answer |
| `TestOTLPHTTPExporterIsNeverRegistered` | absent from `AvailableExporters()` |
| `TestOTLPHTTPDoesNotFollowARedirect` | the credentialled POST never leaves the configured host |
| `TestServerMiddlewarePreservesWriterCapabilities` | Flush works, Hijack reports `ErrNotSupported` |
| `TestClientMiddlewareInjectsAndClonesTheRequest` | the caller's `*http.Request` is untouched |

## Benchmarks

There are none, and therefore no `BENCH.md` (rule 9). The allocation claim this
SDK defends is on `metrics`' observation path; a span is a per-operation object
that allocates by construction, and a benchmark here would pin a number nothing
depends on. Batching (ADR 0051 §Deferred) brings a measurable hot path and a
`BENCH.md` with it.
