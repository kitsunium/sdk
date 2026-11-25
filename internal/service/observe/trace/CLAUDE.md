<!-- updated: 2026-10-05T00:00:00Z -->
# internal/service/observe/trace/

> Reference moved to `internal/service/observe/trace/NOTES.md` (ADR 0253), under the same section titles: Contents, W3C Trace Context — the refusals that look arbitrary, What the OTLP/JSON encoder does that a generated one would not, The OTLP/HTTP emitter is NOT registered, Cost.

## Purpose

The concrete tracing implementation behind `internal/core/observe/trace`: a `Tracer`, the
live span, four samplers, an in-memory `Recorder`, the `RecordError` helper, the
**OTLP/JSON** encoder and OTLP/HTTP emitter for `/v1/traces`, the **W3C Trace
Context** headers read and written (`ParseTraceParent`, `FormatTraceParent`,
`ParseTraceState`, `Inject`, `Extract` — moved here from the core by ADR 0160 §4,
because a wire format is a mechanism), and the two HTTP middlewares. Admitted by
**ADR 0051**.

Everything here is written from the OpenTelemetry and W3C specifications with the
standard library. Nothing imports `go.opentelemetry.io`; nothing imports a
protobuf runtime.

Code range: `0.3.50.*` (ADR 0051), declared in `internal/core/observe/trace` since
ADR 0160 §2 beside the core's own `0.2.20.*`: this package returns those
sentinels as `coretrace.X` and declares none.

## The batch sink

ADR 0051 §Deferred named it: "a batching processor is a sink that buffers and
forwards, where its cost is visible". `BatchSink` is that sink — the batching
span processor of the OpenTelemetry trace SDK specification
(https://opentelemetry.io/docs/specs/otel/trace/sdk/#batching-processor) — and
`Recorder` stays the pull shape a `scheduler` job wants.

- **Zero is the specification, never inert (ADR 0031).** `MaxQueueSize` 2048,
  `ScheduledDelay` 5 s, `ExportTimeout` 30 s, `MaxExportBatchSize` 512, each the
  specification's default; a batch larger than the queue clamps to the queue, as
  the specification requires. `Exporter` has no default and is REFUSED
  (`BatchExporterMissing`, `0.3.50.9`), a typed nil and an uncomparable plug-in
  included, as the registry refuses them.
- **A span end never waits.** It copies the span into a ring slot allocated at
  construction under one short `sync.Mutex` — zero allocations, held by
  `TestBatchSinkRecordingAllocatesNothing` in the race-off alloc lane — and wakes
  the goroutine through a one-slot channel when a batch is full.
- **Loss is counted.** A full queue, or a span ending after `Shutdown`, advances
  `Dropped`; an export that fails advances `Failed` by its span count and reaches
  `OnError`. Nothing is retried here: a retry policy wraps the exporter, where it
  is visible (`OTLPRetryable` is its classification).
- **Every wait is bounded, on the injected clock (ADR 0090).** One goroutine
  exports on a full batch, when `ScheduledDelay` has passed on `Clock` since the
  last export, on `ForceFlush` and on `Shutdown`. An export runs under
  `ExportTimeout`, also on `Clock`: an exporter implementing
  `coretrace.ContextSpanExporter` gets a context that a watcher ends when a
  clock timer fires, so a `ManualClock` decides it and no test sleeps; one that
  implements only `SpanExporter` is bounded by nothing but itself.
  `ForceFlush`/`Shutdown` wait at most until their own context ends and then say
  so with `BatchFlushIncomplete` (`0.3.50.10`), the goroutine finishing on its
  own.
- **A batch is a slice of its own.** An exporter may keep what it is handed, so
  each batch is a fresh slice and its ring slots are zeroed; that copy is the
  ~280 B/op `BenchmarkBatchSinkRecord` reports with 0 allocs/op — one batch
  allocation amortised over 512 spans. BENCH.md §7.

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
- **Do NOT retry inside `Export` or `ExportContext`.** `resilience` owns backoff;
  a hidden one cannot be tuned, and would spend the one round trip's bound
  `ExportContext` is given.
- **Do NOT build the default client without its own `Transport`.** A nil one is
  `http.DefaultTransport`, which any code in the process can empty. Nor replace,
  wrap or clone the `Transport` of a caller-SUPPLIED client: that is theirs.
- **Do NOT drain a response without a bound**, nor by the configured
  `MaxResponseBytes`, which caps memory and would cost a caller who lowered it
  connection reuse on a conforming response.
- **Do NOT forward `Flush`/`Hijack` from `statusRecorder`.** See above.
- **Do NOT decode the encoder's own output in a conformance test.**
- **Do NOT put the path in a span name.**
- **Do NOT build a `tracestate` list here.** The parser reads the header's
  syntax and hands each member to `coretrace.StateBuilder`; the member grammar
  and the list's invariants are the value's, in the core.
- **Do NOT clear undefined trace-flags bits on receipt.** `Sanitized` runs in
  `FormatTraceParent` and nowhere else.

## Verification

| Command | Expected |
|---|---|
| `GOWORK=off go test ./observe/trace/...` (from `internal/service`) | green |
| `bazel test //internal/service/observe/trace:trace_test` | green |
| `TestEncodeOTLPJSONMatchesTheHandWrittenSchemaDocument` | byte-for-byte against the hand-written `.proto` document |
| `TestEncodeOTLPJSONIdentifiersAreHexNotBase64` | the fifth protobuf-JSON rule |
| `TestOTLPAppendJSONIsMarshalsRendering` | the appenders write exactly `otlp.Marshal`'s bytes on 2 000 random trees |
| `TestSamplerIsConsultedOnceAtTheRoot` | exactly one call across a three-level trace |
| `TestAnUnsampledTraceStillPropagates` | zero spans recorded, a valid unsampled context propagated |
| `TestRatioRefusesZeroBecauseZeroIsAmbiguous` | the ADR 0031 answer |
| `TestOTLPHTTPExporterIsNeverRegistered` | absent from `AvailableExporters()` |
| `TestOTLPHTTPDoesNotFollowARedirect` | the credentialled POST never leaves the configured host |
| `TestServerMiddlewarePreservesWriterCapabilities` | Flush works, Hijack reports `ErrNotSupported` |
| `TestParseTraceParentRefusals` | every W3C refusal, each naming its section |
| `TestParseTraceParentIsForwardCompatible` | §3.2.4, including the undashed-tail case |
| `TestFormatTraceParentMasksUndefinedFlagBits` | mask on output, keep on input |
| `TestExtractRestartsTheTraceOnAMalformedParent` / `…KeepsAValidParentDespiteAnUnreadableTraceState` | both halves of §4.3 |
| `TestParseTraceStateRefusals` / `…EnforcesTheThirtyTwoMemberCap` | the whole header refused, never salvaged |
| `TestClientMiddlewareInjectsAndClonesTheRequest` | the caller's `*http.Request` is untouched |
| `TestServerMiddlewareStaysWithinItsPerRequestAllocationBudget` | 11 allocations sampled, 9 unsampled — the §Cost argument, guarded (`!race` lane) |
| `TestRecordingAtCapacityAllocatesNothing` | refusing a span costs less than keeping it (`!race` lane) |
| `TestRecorderTakesAnExclusiveLockOnEveryPath` | `Recorder.mu` is a `sync.Mutex`; see §Cost |
| `TestBatchSink*` (`batch_sink_external_test.go`) | the batching processor on a `ManualClock`: the fill and the delay triggers, the queue clamp, a drop counted, an export cut at `ExportTimeout`, `ForceFlush`/`Shutdown` bounded by their context — no sleep |
| `TestBatchSinkRecordingAllocatesNothing` | a span end into a `BatchSink` allocates nothing (`!race` lane) |
| `TestTheValueASinkReceivesIsFinal` | `finish` TRANSFERS its slices, so nothing that happens to an ended span reaches the exported value |
