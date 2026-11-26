# Notes — <!-- updated: 2026-10-05T00:00:00Z -->

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles. `CLAUDE.md` keeps what to obey; this file holds the rest.

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `tracer.go` | `Tracer` + `NewTracer` + `Start` (the mint/sample/inherit sequence; the span stored on the context with `coretrace.ContextWithSpan`, §The span context Start stores) |
| `config.go` | `TracerConfig` + every clamp, applied once in `resolved()` |
| `span.go` | the recording `span`: mutex-guarded attrs/events/status, idempotent `End`; `finish` TRANSFERS its slices to the exported value rather than cloning them, and `sortedIncoming` skips the clone `SortAttrs` makes when there is one attribute to merge — both in §Cost |
| `noop_span.go` | the span an unsampled trace gets — and why it still carries a context; handed out as `*noopSpan`, the one allocation the context shares |
| `sampler.go` | `AlwaysSample` / `NeverSample` / `ParentBased` / `Ratio` |
| `idgen.go` | `NewTraceID` / `NewSpanID` over `drawTraceID` / `drawSpanID`, which read `crypto/rand` directly: since Go 1.24 it never returns an error (a failing source crashes the program), so the published error is always nil, the tracer's mint cannot fail, and `EntropyFailed` stays declared and published but is returned by nothing. A root span's two ids come from ONE 24-byte read (`drawRootIDs`): a read is a getrandom call whose fixed cost dwarfs its bytes (crypto/rand was 39 % of an unsampled root's `Start`), so the second read bought only overhead — unsampled root `Start`+`End` −8 %, `Sampler_Ratio` −7 %; each half is still checked and redrawn on its own; a child still draws its span id alone |
| `recorder.go` / `recorder_config.go` | `Recorder` + `Sink` / `Collect` / `Len` / `Dropped`; `RecorderConfig` |
| `batch_sink.go` / `batch_sink_config.go` | `BatchSink` — the specification's batching span processor, as a `SpanSink` (#479) — + `NewBatchSink` / `Sink` / `Resource` / `ForceFlush` / `Shutdown` / `Dropped` / `Failed` / `Len`; `BatchSinkConfig` and its four `DefaultBatch*` constants, the specification's own; see §The batch sink |
| `record_error.go` | `RecordError` — the conventional `exception` event |
| `otlp_request.go` | the Go mirror of `collector/trace` and `trace.proto`, in FIELD-NUMBER order; the `common.proto` / `resource.proto` messages and the proto3-JSON scalars are the shared package's |
| `exporter_otlpjson.go` | `EncodeOTLPJSON` + the writer-bound exporter over the shared `otlp.Stream` + `OTLPJSON` (registered, stderr) + the `EXPORT_FAILED` wrap the shared stream leaves under (rendering cannot fail, so it has none) |
| `otlp_append.go` | the tree's `appendJSON` methods and `otlpDocCapacity`: the document written into one buffer with the shared appenders, byte for byte what `otlp.Marshal` renders — `otlp_append_internal_test.go` holds it to that on 2 000 random trees. A 512-span batch encodes in ~0.9 ms with 4 308 allocations, against ~3.3 ms and 6 423 through `encoding/json` (BENCH.md §8) |
| `exporter_otlphttp.go` | `NewOTLPHTTPExporter` — a `coretrace.ContextSpanExporter`: `ExportContext` carries the caller's context into the POST, a done context ending it with the transient `OTLP_EXPORT_UNAVAILABLE`, and `Export` is `ExportContext` under `context.Background()` (#479); `ExportDocument` (`coretrace.DocumentExporter`, ADR 0185) posts a document `EncodeOTLPJSON` wrote earlier — a store-and-forward queue's — through the same sender, an empty one posting nothing; `ExportEncodedDocument` (`coretrace.EncodedDocumentExporter`, #1051) posts one the queue kept gzipped as it rests, labelled `Content-Encoding: gzip` — + `OTLPRetryable` + `OTLPTracesPath` + `otlpSignal`, this signal's `otlp.SignalSpec` — its codes and wording for every verdict the shared sender reaches |
| `otlphttp_config.go` | `OTLPHTTPConfig`, a DEFINED type over the shared `otlp.HTTPConfig` |
| `otlp_export_response.go` | `ExportTracePartialSuccess` reduced to `rejectedSpans` + `decodeRejected` — the one member of the answer that is this signal's own |
| `http_server.go` | `ServerMiddleware` + the semantic-convention keys + `statusRecorder` |
| `http_client.go` | `ClientMiddleware` + `tracedRoundTripper` |
| `numeric.go` | the shortest-round-trip rendering a refused sampling ratio is reported with |
| `traceparent.go` | `ParseTraceParent` / `FormatTraceParent` + `TraceParentHeader` / `TraceStateHeader` / `TraceParentLen` / `VersionSupported` + the fixed-offset reading of §3.2.4 |
| `tracestate.go` | `ParseTraceState` — the header's list syntax; each member goes through a `coretrace.StateBuilder`, which owns the member grammar, the repeated-key refusal and the 32-member cap |
| `propagation.go` | `Inject` / `Extract` over the core's `Carrier` |
| `internal/core/observe/trace` | the codes (range 0.3.50.*) and sentinels — `EntropyFailed` / `InvalidSampleRatio` / `OTLPInvalidSpanContext` / `OTLPSpanNotEnded` / `OTLPEndpointInvalid` / `OTLPExportRejected` / `OTLPExportUnavailable` / `OTLPPartialSuccess` — declared in the core since ADR 0160; this package declares none |

## W3C Trace Context — the refusals that look arbitrary

Moved here from `internal/core/observe/trace` with the parsers (ADR 0160 §4);
the values they produce — the span context, the flags, the `tracestate` list —
and the `Carrier` port stay in the core.

Each is normative, and each is the one a future reader would delete.

| Refused | Section |
|---|---|
| all-zero `trace-id` | §3.2.2.3 "MUST ignore the `traceparent`" |
| all-zero `parent-id` | §3.2.2.4, same |
| version `ff` | §3.2.2.1 "Version `ff` is invalid" |
| uppercase hex | the grammar is `32HEXDIGLC` / `16HEXDIGLC` |
| a header under 55 characters | §3.2.4 "should not parse … should restart the trace" |
| trailing content on version `00` | version 00 has no extension point |
| a higher version whose tail is not dash-delimited | §3.2.4 — without it, a 56th hex byte reads as a TRUNCATED flag field, so the sampled bit is wrong rather than the header rejected |

**Flags are masked on OUTPUT, never on input.** §3.2.2.5.2 says a vendor MUST
zero the undefined bits; §3.2.4 says a receiver must not assume anything about
unknown fields. Clearing on receipt satisfies the first and violates the second.
`TraceFlags.Sanitized` runs in `FormatTraceParent` and nowhere else.

**`Extract` returns no error.** §4.3 prescribes exactly one response to a
malformed parent — start a new trace, delete the tracestate — so there is nothing
to decide. An error would invite the response the specification forbids: failing
a request because a stranger wrote a bad header. `ParseTraceParent` is the
typed-error form, for diagnosis.

Three §4.3 consequences, each tested:

- a malformed **traceparent** drops the tracestate with it;
- a malformed **tracestate** does NOT drop the traceparent;
- **two** traceparent headers merge to `"v1,v2"`, fail the grammar, and restart —
  which is correct, because neither claim may be believed.

The list is the core's `StateValue`, ordered because §3.5 makes the order
meaning; its limits are the grammar's own, enforced by the core's
`StateBuilder` as the parser hands it each member.

**One leniency, and it is the only one**: OWS is stripped at the list's edges,
where the `list` rule grants no OWS slot, because RFC 7230 §3.2.4 already
normalises a field value's surrounding whitespace. The `nblk-chr` rule is
enforced in the core's `Insert` (and `StateBuilder.Add`) instead. Documented in place and in
`TestTraceStateAbsorbsEdgeWhitespaceButInsertRefusesIt`.

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

**The tree is rendered by appenders, held to `otlp.Marshal` byte for byte.**
`EncodeOTLPJSON` builds the tree as before — both refusals happen there — then
writes it with `otlp_append.go`'s `appendJSON` methods and the shared appenders
into one buffer sized by `otlpDocCapacity`, instead of `encoding/json`'s
reflection and a fresh `[]byte` per timestamp. `otlp_append_internal_test.go`
compares the two renderings on 2 000 random trees; a field added to the tree is
added to its `appendJSON` in the same change, or that test fails.

## The OTLP/HTTP emitter is NOT registered

`otlpjson` self-registers on **stderr** (ADR 0030). `NewOTLPHTTPExporter` is
registered nowhere, and the reason is one step beyond ADR 0030 rather than a
matter of degree: **there is no endpoint that could be a correct default**, so a
registered emitter would POST at whatever answers on an address the caller never
named — inside a cluster, a real host belonging to somebody else.
`TestOTLPHTTPExporterIsNeverRegistered` pins the absence.

Its transport is `internal/service/observe/internal/otlp`'s `Sender`, shared with
`internal/service/observe/metrics`: the two emitters were near-copies, and they differed
only in their codes, their wording and the name a partial success counts under,
which this package hands over as `otlpSignal`. Every verdict still carries
`0.3.50.*` and this package's words, byte for byte, and every test below ran
unchanged across the move. What follows is enforced there and restated here.

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

The bounded response read covers the drain too: after the verdict, what is left
of the body is drained through `DefaultOTLPMaxResponseBytes` — the default, not
the configured cap, which bounds memory — and closed. Unbounded, a collector
streaming an endless body held `Export` forever whenever the client had no
deadline.

**The default client owns its connection pool, because a shared one gave wrong
verdicts.** It used to ride `http.DefaultTransport`, and net/http puts a
BODILESS response's connection back in the idle pool *before* handing the
response to the waiting round trip; a `CloseIdleConnections` on that pool
landing in between closes the connection under it, and the round trip reports
`HTTP/1.x transport connection broken: http: CloseIdleConnections called` for an
answer that had already arrived. Every `httptest.Server.Close` and every
`http.DefaultClient.CloseIdleConnections` in the process is such a call. The
verdict was then `OTLP_EXPORT_UNAVAILABLE` whatever the collector said: a 200
became retryable — the caller's retry replays spans the collector accepted — a
429 lost its `Retry-After`, a 413 became retryable. It was found as a flake in the
metrics sibling, whose tests run in parallel; the code here was identical, and
this file's older tests hid it only by running sequentially.
`TestOTLPHTTPDefaultClientOwnsItsConnectionPool` proves the isolation
deterministically.

The pool is a clone of `http.DefaultTransport` taken at construction, proxy
environment included; where that default has been replaced by something other
than an `*http.Transport`, a fresh transport with `Proxy:
http.ProxyFromEnvironment` and net/http's 90 s idle timeout stands in.
`TestOTLPHTTPDefaultClientHonoursTheProxyEnvironment` drives both branches
through a real `HTTP_PROXY` in a CHILD process, because net/http reads the proxy
environment once per process and never proxies loopback;
`TestOTLPHTTPProxyChild` self-skips unless `KTN_OTLP_TRACE_PROXY_CHILD` is set, so
it needs no tag and no compensating lane (CLAUDE.md rule 12). A caller-SUPPLIED
client is used as given, `Transport` included — one riding `http.DefaultTransport`
keeps the exposure, and a `Transport` of its own removes it. The `SpanExporter`
port has no lifecycle method, so nothing closes the pool: idle connections are
reaped by the idle timeout, and one exporter per collector, built once, is the
intended shape.

## Cost

Measured, in `BENCH.md`. The two units named here as unmeasured now are, and
both produced a change.

### A traced HTTP request

| | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| the same handler, no middleware | 5.1 | 0 | 0 |
| root (no inbound header), sampled | 2 098 | 1 720 | **12** → **11** |
| root, unsampled | 1 154 | 888 | **10** → **9** |
| child (valid `traceparent`), sampled | 2 346 | 1 736 | **13** → **12** |
| child, unsampled | 1 391 | 904 | **11** → **10** |

The arrows are §The span context Start stores: the context holds the span
itself rather than a boxed copy of its identity, one allocation fewer on every
row (BENCH.md §The span context Start stores). The other columns are the original measurement.

Against a full SDK HTTP request measured on the **same box**
(`//internal/service/net/server`'s `BenchmarkHTTP_Adapter`, 162 900 ns /
5 148 B / **63 allocs**), tracing costs **+1.3 % of latency and +19 % of the
allocation rate**. Latency and allocation rate give opposite answers and only
the second one decides anything: nobody rejects a middleware over 1.3 %, while
a fifth more allocations per request is GC pressure paid by every goroutine in
the process. That is the number to put in front of a team asking whether tracing
belongs in a **default** middleware stack, and
`TestServerMiddlewareStaysWithinItsPerRequestAllocationBudget` guards it.

It also prices a sampling ratio honestly: an unsampled request is 55 % of a
sampled one in TIME and 83 % of it in ALLOCATIONS, because ten of the twelve are
paid before anything is recorded (nine of eleven since §The span context Start
stores). Turning the rate down buys much less than the latency figures suggest.

### The span context Start stores

`Start` puts the new span on the context with `coretrace.ContextWithSpan`, which
stores the SPAN — a pointer — where `ContextWithSpanContext` boxed a copy of its
`SpanContextValue`, too wide for an interface word. `SpanContextFromContext`
reads it back through `SpanContext()`, which is lock-free and immutable on both
span types. An unsampled span is now `*noopSpan`, so the span and the context
share its one allocation: `Start`+`End` unsampled 3 → 2 allocations and −17 %
time, sampled 3 → 2 and −9 % (`pkg/v1/observe/trace` BENCH.md §The shared span
context). The context keeps the span reachable for as long as it lives; `finish`
already hands the span's attributes and events to the exported value and nils
them, so what a context retains is the span's fixed fields and its links.

### `Recorder.record` — the lock was the wrong one

`record` takes the exclusive side on every span end from every request
goroutine; `Len` and `Dropped` take the shared side once per collection
interval; and `Collect`, which is what an export actually calls, takes the
exclusive side too. The field held a `sync.RWMutex`, so a read-write lock was
optimising the rarest path and taxing the busiest one. It is a `sync.Mutex` now.

| goroutines | `Mutex` | `RWMutex` | RWMutex costs |
|---:|---:|---:|---:|
| record at capacity, 1 → 8 | 33.2 → 130.6 ns | 52.9 → 166.3 ns | **1.27×–1.65×** |
| record while an operator polls `Len`, 1 → 8 | 83.3 → 136.8 ns | 228.4 → 1 228 ns | **2.7×–9.0×** |
| `Len` with 4-8 concurrent readers | 79.5 → 93.4 ns | 66.2 → 75.9 ns | RWMutex **1.20×–1.23× faster** |

The middle row is the one that settles it, because it is the scenario the
RWMutex's own field comment named. The bottom row is what the change **cost**,
stated rather than hidden: with four or more goroutines doing nothing but
polling, the rejected lock wins — on a scenario this type's contract excludes,
since `Collect` DRAINS and a Recorder therefore has exactly one reader. At that
one reader the `Mutex` is marginally faster anyway.

`TestRecorderTakesAnExclusiveLockOnEveryPath` fails the build on a change back,
and the rejected design stays in `recorder_bench_test.go` as a control so the
price is still re-runnable.

### What was refused

Three further allocations were priced and left in place, each because removing
it would weaken a stated property — the full argument is in `BENCH.md` §4:

- **`http.Header.Get("traceparent")`**, 87 ns and 1 allocation per header read,
  purely because `TraceParentHeader` is lowercase. It must be, for `Inject`;
  the fix is a second lookup-only constant in `internal/core/observe/trace`, not here.
  The local workaround is a `Carrier` adapter, which is exactly the thing
  `serveTraced`'s comment says this middleware deliberately does not have.
- **The doubled span context**, 4 of the 12 allocations and the largest group in
  the profile — 3 of 11 since `Start`'s half stopped boxing (§The span context
  Start stores); the extracted parent's half is still a boxed value. It is the
  price of having exactly ONE inheritance path; removing it either changes which
  span parents a request that carries no `traceparent`, or reopens the
  hand-passed parent `Start`'s doc refuses by name.
- **Pooling `&statusRecorder{}`**, 1 allocation. It is handed to a
  caller-supplied handler, and this type is the one ADR 0051 records as the
  place ADR 0047's defect came back one layer up. A pool adds use-after-return
  to that family for one object.

### Batching

ADR 0051 §Deferred brought a third unit, and #479 measured it: a span end
into a `BatchSink` costs 238-273 ns and **0 allocations** with the exporting
goroutine draining behind it, 265-344 ns from every P at once. BENCH.md §7.
