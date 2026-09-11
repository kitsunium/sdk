# ADR 0051 — the `trace` domain: OpenTelemetry's third pillar, from the document

- **Status**: Accepted
- **Date**: 2026-09-10
- **Deciders**: SDK maintainers
- **Related**: [ADR 0044](0044-metrics-adopts-the-otel-data-model.md) (the OTel data model, adopted without the code — the template this follows), [ADR 0048](0048-sdk-metrics-otlp-json.md) (OTLP/JSON, the encoder/emitter split and the four protobuf-JSON rules), [ADR 0029](0029-sdk-net-domain.md) (the network domain these middlewares plug into), [ADR 0030](0030-stdout-is-a-protocol-channel.md) (what a registered exporter may arm), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (clamp vs refuse), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (sibling interfaces), [ADR 0041](0041-sdk-scheduler-domain.md) (func ports), [ADR 0047](0047-sdk-net-websocket.md) (the hijack defect this middleware must not re-create)
- **Closes**: part of ADR 0044 §Deferred — "Exemplars … the field lands with the first trace context". The trace context now exists; §Deferred below states exactly what `metrics` needs from this domain to finish the job.

## Context

The SDK has two of observability's three pillars. `logger` says WHAT happened,
`metrics` says HOW MUCH — and since ADR 0044 it says it in the OpenTelemetry data
model, on an OTLP wire it can carry losslessly (ADR 0048). Neither says WHERE, and
"where" is the question a distributed system actually raises: a request that took
four seconds spent them in one of eleven services, and no counter in any of them
can say which.

The maintainer's instruction was one sentence — **"I want OTEL in the SDK"** — and
`metrics` already answered it once. That answer is the template, not an analogy:

- OpenTelemetry is a **published specification**. ADR 0044 implemented its metrics
  data model from the document, with zero `go.opentelemetry.io` imports, for the
  same reason this SDK implements the Prometheus exposition format, RFC 7517, the
  JWS Compact Serialization and a five-field POSIX cron from theirs.
- ADR 0048 then wrote the OTLP/JSON wire by hand from the `.proto` files, with
  `encoding/json` and `net/http`, and pinned it against expected bytes assembled
  from the schema rather than from the encoder's own output.

The dependency argument is unchanged and is restated here because it is the first
question anyone asks. `go.opentelemetry.io/otel` plus `otel/sdk` plus `otel/trace`
introduces **`x/sys`**, which this SDK bans SDK-wide (ADR 0022 / ADR 0034), plus a
release cadence this repo does not control — in exchange for a data model whose
whole content is a shape. **Interoperability is a property of the wire, not of the
import graph.** A span that carries a trace id, a span id, a kind, a status and
typed attributes, and that leaves on an OTLP/JSON body, is accepted by every
collector in the ecosystem. Nothing about that requires importing the ecosystem.

Tracing adds one thing metrics did not have: a **propagation format**. W3C Trace
Context is a Recommendation with an ABNF grammar, and it is where the interesting
failures live — a header written by a stranger, parsed on the hot path of every
inbound request, whose malformed cases have prescribed behaviour that is easy to
get subtly wrong in ways nobody notices for months.

## Decision

### 1. `trace` is the 17th core sibling, in the ordinary four-layer shape.

`internal/core/trace` declares the port and the data model, `internal/service/trace`
implements it, `pkg/v1/trace` publishes it. Error blocks `0.2.20.*` and `0.3.50.*`,
allocated in `codeRangeOwners` in this change (ADR 0035).

**There is a registry, and it earns its keep.** `SpanExporter` registers by name
exactly as `metrics.Exporter` does — reachable by blank import, addressable from
configuration. It also earns its keep a second way that is specific to this
change: §Decision 5 requires the OTLP/HTTP emitter to be **absent** from it, and
"absent from the registry" is only a statement anyone can check if there is a
registry to be absent from. A test pins the absence.

There is **no** registry for samplers. A sampler is a policy, not a named plug,
and its key would be a string nobody types twice.

### 2. The attribute, Resource and Scope model is REUSED, not twinned.

`core/trace.AttrValue`, `ResourceValue` and `ScopeValue` are Go **type aliases** of
the types `internal/core/metrics` declares. `pkg/v1/trace.Attr` and
`pkg/v1/metrics.Attr` are therefore one type, and a value built by either package
is accepted by both.

The import reads backwards until you look at what those types are. They are not
metrics concepts: `AttrValue` is `opentelemetry/proto/common/v1`'s
`KeyValue`/`AnyValue`, `ResourceValue` is `resource/v1.Resource`, and `ScopeValue`
is `common/v1.InstrumentationScope`. All three live in `common.proto` and
`resource.proto` **because every signal shares them**. They sit in `core/metrics`
today only because metrics was the first signal this SDK implemented.

A twin would have been the tidier import graph and the worse type system. Three
costs, none hypothetical:

- `metrics.String("k","v")` and a twinned `trace.String("k","v")` would be two
  incompatible values spelling one fact, so every consumer instrumenting both
  signals writes the conversion function this SDK declined to.
- A process would carry **two Resources that can disagree**. `service.name` is the
  key a backend correlates a trace with a metric on, and "correlate these when the
  two structs happen to hold the same string" is not a guarantee.
- **Exemplars** — the one thing ADR 0044 §Deferred said it was waiting on a trace
  domain for — attach a trace id to a metric data point. With one attribute model
  that is a field; with two it is a conversion at the boundary of the very feature
  the split would exist to keep clean.

**The extraction that would make the graph read forwards is named and deferred**: a
shared `internal/core/otel` holding `common.proto`'s types, with `core/metrics` and
`core/trace` both above it. It is not done here because it would MOVE types
published through a `pkg/v1/metrics` alias — spending the ADR 0040 v0 licence on a
rename that changes no behaviour, at the same moment several other domains are
landing on the same tree. It is a refactor, and it should be its own change.

One thing is deliberately **not** shared: `DefaultScopeName`. An
`InstrumentationScope` names the library that produced **this** signal, so stamping
a span batch with `…/pkg/v1/metrics` would tell a backend that the metrics package
emitted spans. `core/trace` declares its own constant and its own `NormalizeScope`.
That is the line where sharing stops being reuse and starts being a wrong answer,
and it has a test.

### 3. W3C Trace Context, and a malformed header NEVER fails a request.

The `traceparent` grammar is implemented from the ABNF, refusal by refusal, with
the section cited beside each one. The cases that look arbitrary are exactly the
ones a future reader would delete, so they are the ones the tests name:

| Refused | Section |
|---|---|
| an all-zero `trace-id` | §3.2.2.3 — "MUST ignore the `traceparent`" |
| an all-zero `parent-id` | §3.2.2.4 — same |
| version `ff` | §3.2.2.1 — "Version `ff` is invalid" |
| uppercase hex anywhere | the grammar is `32HEXDIGLC` / `16HEXDIGLC` |
| a header shorter than 55 characters | §3.2.4 — "should not parse … should restart the trace" |
| trailing content on version `00` | version 00 has no extension point |
| a higher version whose tail is not dash-delimited | §3.2.4 — each field is followed by a dash or end-of-string |

**Forward compatibility is implemented, not assumed.** §3.2.4 requires a higher
version to be parsed from its first 55 characters, because every version keeps them
where `00` puts them. Without the dash check on the tail, a 56-character header
whose 56th byte is a hex digit reads as a valid context with a **truncated flag
field** — a wrong sampled bit rather than a rejected header, which is the failure
mode that silently halves a trace.

**Undefined flag bits are masked on the way OUT and preserved on the way IN.**
§3.2.2.5.2 says a vendor MUST set them to zero, and §3.2.4 says a receiver must not
assume anything about unknown fields. Clearing on receipt satisfies the first and
violates the second; masking at format time satisfies both, and it is the only
combination that does.

`Extract` returns **no error**, and that is a decision rather than an omission.
§4.3 prescribes exactly one response to a malformed `traceparent` — "the vendor
creates a new `traceparent` header and deletes `tracestate`" — so there is nothing
for a caller to decide. Handing them an error would invite the one response the
specification forbids: failing a request because a stranger wrote a bad header,
which is a denial of service with extra steps. `ParseTraceParent` is the
typed-error form, for a caller who is diagnosing rather than serving.

Three consequences of §4.3, each with a test:

- A malformed `traceparent` **drops the `tracestate` with it**. The vendor list
  describes a trace this process is about to replace; keeping it would attach
  somebody else's state to a brand-new trace id.
- A malformed `tracestate` does **not** drop the `traceparent`. §4.3 makes
  validating it a MAY and permits discarding just that header; losing a valid
  parent over an unreadable annotation breaks the trace to protect a decoration.
- **Two `traceparent` headers** merge under RFC 7230 field order into
  `"value1,value2"`, which fails the grammar and restarts the trace. That is the
  correct outcome — two upstreams claiming different parents is not a case where
  either may be believed — and it falls out of the grammar rather than needing a
  rule of its own.

`tracestate` is an **ordered, immutable list**, not a map, because §3.5 says the
order is meaning: "the order of unmodified key/value pairs MUST be preserved" and a
"modified key SHOULD be moved to the beginning (left)". Leftmost is the system that
touched the trace most recently, and a map loses exactly that, silently. The
grammar's own bounds are the bounds — 32 members, keys ≤256 (or `tenant@system`
with 241/14), values ≤256 printable ASCII excluding `,` and `=` — so there is no
SDK-invented ceiling, which is ADR 0031 §refuse applied to a limit somebody else
already chose non-arbitrarily.

**One deliberate leniency, stated because it is the only one**: OWS is stripped
around every list member, including at the list's edges where the `list` rule
grants no OWS slot. A strictly-positioned parser would refuse `"a=1 "` — a value's
last character must be `nblk-chr`. RFC 7230 §3.2.4 already requires a recipient to
strip leading and trailing whitespace from a field value *before* it is a
field-value, so the strict check could only fire on input the HTTP layer is
specified to have normalised, and it would pay for that by discarding another
vendor's whole list over whitespace nobody can see. The `nblk-chr` rule is enforced
in `Insert` instead, on the write side, where it still prevents something.

This SDK **writes no `tracestate` entry of its own**. It is not a tracing vendor
with state to carry, and inventing a key would put a name nobody registered on
every outbound request. `Insert`/`Delete` exist for a consumer who *is* one.

### 4. The sampling decision is taken ONCE, at the root.

This is the load-bearing rule of the domain. A `Sampler` is consulted when a trace
STARTS and at no other moment; every child span, in this process and in every
process downstream, inherits the answer through the `sampled` bit of the
`traceparent`.

It is not an optimisation. Deciding per span produces a trace with **holes in the
middle**, and a trace with holes is worse than no trace: a span whose parent was
dropped becomes an orphan the backend renders as its own root, so one request
appears as several unrelated ones and the latency of the whole is unrecoverable.
The failure is also invisible at the process that causes it — nothing there looks
wrong. A test counts the sampler's invocations across a three-level trace and
requires exactly one.

An **unsampled span is a no-op that still carries a context**. That is why
`Start` never returns nil: the traceparent still goes out, with the sampled bit
CLEAR, so the next service inherits the decision instead of taking a second,
contradictory one. Without it, one dropped trace becomes N kept ones — the opposite
of sampling — and every instrumentation site grows an `if span != nil`.

Four policies ship: `AlwaysSample`, `NeverSample`, `ParentBased` and `Ratio`.
`Ratio` is deterministic on the trace id's last 8 bytes (the tail, because an id
that carries structure carries it at the front), so two services that both start a
root for the same id reach the same verdict and a trace is never half-kept.

**A `Sampler` is a FUNC port**, applying ADR 0041 structurally: a published func
type cannot grow a method at all, so there is nothing to widen and no sibling to
invent later. `SpanSink` is a func port for the same reason.

The three-valued OTel `SamplingDecision` — DROP / RECORD_ONLY / RECORD_AND_SAMPLE —
is **deferred by name**. RECORD_ONLY needs a second export path and a second
meaning for the `sampled` bit, and nothing in this SDK consumes a
recorded-but-not-sampled span.

### 5. A sampling rate of 0 is REFUSED, because it means two things.

This is ADR 0031's question, and this domain is where it has teeth.

Does a rate of `0` mean "sample nothing" or "nobody configured this"? It means
**both**, and nothing in a `float64` can tell them apart. An unset struct field, a
JSON document missing the key, a `rate:` with nothing after it — all three produce
`0.0`. A `Ratio(0)` that honoured the number would disable tracing for a deployment
that believed it had configured it, and the symptom is the **absence of telemetry**:
no error, no log line, and nothing to alert on, because "no traces" and "a quiet
service" look identical.

So the ambiguity is not resolved, it is refused. `Ratio` returns
`InvalidSampleRatio` (`0.3.50.2`) for `0`, for a negative, for anything above 1 and
for NaN. `NeverSample` is the name for none, and it cannot be produced by
forgetting anything; `AlwaysSample` (or `Ratio(1)`) is the name for all.

The rest of the domain's zero values **clamp**, and the difference is the ADR 0031
test applied honestly — a clamp is right exactly when the SDK is not substituting
judgement:

| Unset | Behaviour | Why |
|---|---|---|
| `TracerConfig.Sampler` | clamps to `ParentBased(AlwaysSample)` | the safe direction; a tracer that silently dropped everything would make a misconfiguration look like a healthy quiet service |
| `TracerConfig.Clock` | clamps to `clock.System` | "the real clock" is a description, not a choice |
| `SpanParams.StartTime` | clamps to now | same |
| `SpanParams.Kind` | clamps to `INTERNAL` | the schema's own recommended default |
| `RecorderConfig.MaxSpans` | clamps to 2048 | a buffer nobody drains is how a tracing integration takes a process down; there is no "unbounded" setting |
| `TracerConfig.Sink` | records nothing | a tracer with no destination has nowhere to put a span, and buffering into a slice nobody drains is the leak that shape invites |
| a `Temporality`-shaped case: `SpanKind` cast from an integer | clamps | an unstated kind changes only how a span is DRAWN, unlike a temporality, which changes what a NUMBER means |

### 6. OTLP/JSON on `/v1/traces`, on ADR 0048's exact template.

`EncodeOTLPJSON(SpansValue) ([]byte, error)` is a **pure** snapshot→bytes function
that touches no socket. `NewOTLPHTTPExporter(name, cfg)` calls it and adds only
transport. The boundary is the byte slice, for the reason ADR 0048 gives: an
encoding defect and a network defect have different reproductions, different
evidence and different fixes, and a single `Export` that did both would make every
OTLP question start with "is the collector up?".

`otlp_request.go` is a Go mirror of `trace.proto`, `common.proto` and
`resource.proto` in **field-number order**, because that order is derivable from
the document and is what makes the hand-written expected bytes checkable line by
line. The visible evidence that the order came from the schema and not from taste:
`otlpSpan` puts **`flags` last, after `status`**, because it is field 16 and status
is 15 — even though every `.proto` listing prints `flags` beside `parent_span_id`
where it reads naturally. It is the same tell as `NumberDataPoint`'s
attributes-after-value in the metrics mirror.

ADR 0048's four protobuf-JSON rules apply unchanged (lowerCamelCase names, 64-bit
integers as decimal strings, enums as integers, unknown fields ignored). This
signal adds a **fifth, and it is the one a generated encoder gets wrong**:

> "The `traceId` and `spanId` byte arrays are represented as case-insensitive
> hex-encoded strings; **they are not base64-encoded** as is defined in the standard
> Protobuf JSON Mapping."

A base64 identifier is 24 characters of plausible-looking text that no collector
accepts, so the failure is silent unless something asserts the alphabet. A test
asserts both halves: the hex is present, and the base64 of the same 16 bytes is
not.

A second trap that is specific to this signal: **not every integer is 64-bit**.
`flags` is `fixed32`, so it rides as a plain JSON **number**; widening it to a
string is exactly as wrong as narrowing a timestamp to a number.

Presence decisions, in the same spirit as ADR 0048 §Decision 3:

- **`flags` is always emitted.** It is `fixed32` with no presence and would
  legitimately be omitted at zero — but it carries the span's sampled bit and
  whether its parent was REMOTE, and a field that vanishes precisely when it
  carries the surprising answer is one a reader cannot trust. It is also never
  actually zero here, because `SPAN_FLAGS_CONTEXT_HAS_IS_REMOTE_MASK` is always
  set: without that bit a receiver cannot tell "the parent is local" from "this
  producer does not track remoteness", and this producer does.
- **`status` is omitted when UNSET.** This is the opposite call from the metrics
  encoder's three always-emitted fields, and the difference is presence: `asInt`,
  the histogram `sum` and `isMonotonic` each carry information their absence would
  destroy, while `STATUS_CODE_UNSET` is the schema's default and an all-default
  `Status` message carries exactly what its absence carries. `UNSET` also does not
  mean "it worked" — it means no judgement was recorded, and a backend applies its
  own heuristics — so emitting it adds a message to every span to say nothing.
- **`parentSpanId` is omitted on a root.** The schema's spelling for "no parent" is
  an absent field, not sixteen zero digits — which would be the *invalid*
  identifier W3C Trace Context declares.

Two refusals, both of STRUCTURE and therefore failing on the first export or never:

- a span whose trace-id or span-id is all zeroes — `OTLPInvalidSpanContext`
  (`0.3.50.3`). The schema requires 16 and 8 real bytes and W3C declares the
  all-zero form of each invalid, so there is no honest hex to emit.
- a span with no `EndTime` — `OTLPSpanNotEnded` (`0.3.50.4`).
  `end_time_unix_nano` is required, and a zero would claim the span ended at the
  Unix epoch, which renders as a span 56 years long.

The emitter is a **connector**, written like `nettransport` and like the metrics
one: bounded response read (1 MiB) and a bounded drain after it, a default client
that owns its own connection pool rather than riding `http.DefaultTransport`
*(amended 2026-09-11 — see ADR 0048 §7 for the `net/http` race that made a
received answer read as a transport fault, and why that double-counts on a
retry)*, no redirects (CWE-918 — an unfollowed `30x` is
classified as the permanent rejection a misconfigured endpoint is), an endpoint
refused at construction unless it is an absolute `http(s)` URL with a non-root path
(a bare `http://collector:4318` connects, answers 404, and looks exactly like a
collector that is up), and **no retry**. It classifies instead, in exactly
`resilience.RetryConfig.Retryable`'s shape, so the caller owns a backoff they can
see, tune and cancel. The retryable set is spelled out (429/502/503/504 and a
transport fault) rather than derived from the status class, because **5xx is not
retryable as a class**: a 500 or a 501 means the same request will fail the same
way.

An **empty batch is a no-op**, not a POST. A request carrying zero spans costs a
round trip to say nothing, and an export loop on a quiet service would make one
every interval forever.

`otlpjson` registers on **stderr** (ADR 0030). **`NewOTLPHTTPExporter` is never
registered**, and the reason is one step beyond ADR 0030 rather than a matter of
degree: there is no endpoint that could be a correct default, so a registered
emitter would POST at whatever answers on an address the caller never named —
inside a cluster, a real host belonging to somebody else. A test pins its absence
from `AvailableExporters()`.

### 7. The middlewares are the network domain's OWN middleware type.

`ServerMiddleware` returns `corenet.Middleware[http.Handler]` and
`ClientMiddleware` returns `corenet.Middleware[http.RoundTripper]`. Both are the
generic type ADR 0029 already declares, instantiated — so they compose with
`corenet.Chain` rather than beside it, and no new middleware vocabulary is
introduced.

They decorate `http.Handler` / `http.RoundTripper` rather than the group's
`ConnHandler`, because **a traceparent is an HTTP header**: the connection
middleware sees bytes, and there is no header at that level to extract. That is a
statement about where the propagation format lives, not a gap in the net domain.
The client one sits at the **transport** for the same reason ADR 0029 puts the
Policy there: there is no path to the network that skips it, so "every egress
carries a traceparent" stops being a convention the next contributor has to
remember.

`core/trace.Carrier` is `http.Header`'s `Get`/`Set` pair, exactly — so
`trace.Inject(ctx, req.Header)` compiles with **no adapter**, and `core/trace`
still imports no `net/http`, which would drag an HTTP opinion into a contract that
also has to serve a message queue and a gRPC metadata map. It is FROZEN at two
methods (ADR 0039); a test holds the assignment so a third method stops compiling.

Four decisions inside the middlewares:

- **The server span is named by the METHOD alone.** A span name is the
  low-cardinality label a backend groups on; a path carries identifiers, so
  `GET /users/42` and `GET /users/43` are two operations to a backend and one to a
  human, and a service with a million users would have a million span names. The
  conventions ask for `{method} {route}` where route is the *template*; this SDK
  does not route, so it has no template and does not invent one from the path. The
  path is recorded as an attribute, where high cardinality is affordable — and it
  is the **escaped** form, for the reason `corenet.RequestValue` gives: `url.URL.Path`
  is already percent-decoded, so recording it would report `%2e%2e` as `..`.
- **4xx is an error on a CLIENT span and not on a SERVER span.** The OTel HTTP
  conventions make exactly this asymmetry: a 404 is the caller asking for something
  that is not there, which is the server working correctly, and marking it ERROR
  would make every scanner probing for `/wp-admin` light up a service's error rate.
  On a client span the call failed whatever it says about whose fault it is.
- **The status recorder implements `Unwrap` and NOTHING ELSE.** The tempting
  alternative — declaring `Flush` and `Hijack` that forward — would make the
  wrapper claim both capabilities *unconditionally*, so `w.(http.Hijacker)` would
  succeed on a writer that cannot hijack and the failure would surface inside a
  protocol upgrade rather than as a clean "not supported". **That is the exact
  defect class ADR 0047 fixed in the listener engine, re-introduced by a
  middleware.** `http.ResponseController` walks the `Unwrap` chain and asks the
  real writer, which is how this SDK's own SSE and WebSocket implementations
  already find `Flush` and `Hijack`. Both directions have a test.
- **The client middleware CLONES the request.** `http.RoundTripper`'s contract says
  an implementation "should not modify the request", and net/http retries an
  idempotent request on a fresh connection using the same `*http.Request` — so a
  header set in place would be observed by the caller afterwards, and a
  span-per-attempt would write a different traceparent onto a request they still
  hold.

### 8. `Span` is frozen at five methods, and `RecordError` is a helper.

`Tracer` is one method, `Span` is five, both FROZEN (ADR 0039). Two things a reader
will look for and not find:

- **No `RecordError` on the port.** It would have to decide what an error's TYPE
  is, which is a judgement about the caller's error model, and it is expressible as
  one `AddEvent` call. It ships as a package-level helper instead — which is the
  shape that does not freeze a decision into a port every downstream implementer
  has to satisfy. It writes the conventional `exception` event and marks the span
  ERROR; `exception.type` carries the errs **dotted-quad code** when the error is
  an SDK error (a code is the stable identity an operator greps across a trace, a
  log line and an alert; `%T` names a Go type nobody outside the process can act
  on) and is omitted otherwise. `exception.stacktrace` is never set: a Go error
  carries no stack, and one taken at the recording site would name the wrong
  goroutine — a plausible wrong answer, which is worse than an absent one.
- **`End` takes no timestamp.** A span that ends at a time other than "now" is
  replaying history or working around a clock, and both want a sibling interface
  rather than a parameter every caller passes a zero value to.

`End` is **idempotent**. The common way to call it twice is a `defer span.End()`
beside an explicit `End` on an early return, and a duplicate span is a duplicate row
in every backend — the duplicate nobody notices. Writes after `End` are dropped
rather than raced or silently lost.

## Consequences

- Three new packages: `internal/core/trace` (the model, the W3C format, the ports,
  the exporter registry), `internal/service/trace` (tracer, spans, samplers,
  recorder, OTLP/JSON encoder, OTLP/HTTP emitter, HTTP middlewares) and
  `pkg/v1/trace` (aliases + helpers, `README.md` generated by gomarkdoc per rule
  10). The root `CLAUDE.md` domain count goes from sixteen to seventeen.
- **Error codes**: `0.2.20.1`–`0.2.20.6` and `0.3.50.1`–`0.3.50.8`, with both
  ranges added to `codeRangeOwners` in this change (ADR 0035).
  `docs/error-codes.yaml` regenerated.
- **The dependency budget is unchanged**: `context`, `crypto/rand`, `encoding/binary`,
  `encoding/hex`, `encoding/json`, `io`, `math`, `maps`, `net/http`, `net/url`,
  `os`, `slices`, `strconv`, `strings`, `sync`, `time` — plus `internal/kernel/{clock,errs,snapshot}`
  and `internal/core/{metrics,net}`. Nothing from `go.opentelemetry.io`, no protobuf
  runtime, no `x/sys`.
- **The conformance test does not test the encoder against itself.** The expected
  document is written by hand from `trace.proto`, `common.proto`, `resource.proto`
  and the OTLP §JSON Protobuf Encoding section, with the field numbers named in
  comments beside each fragment. A test that decoded the encoder's own output would
  prove self-consistency — exactly the property a wrong field name, a base64
  identifier or a mis-numbered enum preserves.
- **No benchmark, and therefore no `BENCH.md`** (rule 9). The allocation claim this
  SDK defends is on `metrics`' observation path; a span is a per-operation object
  that already allocates by construction, and a benchmark here would pin a number
  nothing depends on. When batching lands (§Deferred) it brings a measurable
  hot path and a `BENCH.md` with it.
- **No `//go:build !race` file and no `gazelle:excluded` target**, so this change
  adds no exclusion and needs no compensating lane (rule 12). Every test in it runs
  in the ordinary race-on suite.

## Deferred

Named here so a future reader finds the reason rather than the gap.

- **Exemplars — and what `metrics` needs from this domain to get them.** This is
  the one deferral with a concrete hand-off, because ADR 0044 §Deferred pointed at
  it by name ("an exemplar would have no span id to hold; the field lands with the
  first trace context"). It now has one. What `metrics` needs, precisely:
  1. `core/metrics` gains an `ExemplarValue` — `{FilteredAttrs []AttrValue,
     Time time.Time, Value (int64|float64), TraceID [16]byte, SpanID [8]byte}` —
     which is `metrics.proto`'s `Exemplar`, fields 7/2/3/4/5/6 in field-number
     order. It must hold the **raw arrays**, not `trace.TraceID`: `core/metrics`
     must not import `core/trace`, which would invert the dependency this ADR
     established in §Decision 2.
  2. The observation path needs the **current span context** at record time. A
     `Meter` method taking a `context.Context` would change the frozen `Meter`
     port, so it lands as an ADR 0039 **sibling** — an `ExemplarMeter` whose
     instrument fetch accepts a context — or as an explicit `WithExemplar` on the
     instrument. Reading it from a package-level "current span" is the one option
     that must not be taken: there is no ambient span in this SDK, deliberately.
  3. A **filter**. The OTel default keeps an exemplar only for a SAMPLED span,
     which is the whole point — an exemplar into a trace nobody stored is a dead
     link. `SpanContextValue.IsSampled` is that predicate, and it is already the
     only sampling question anything asks.
  4. Both encoders gain the field: OTLP/JSON emits `exemplars` (field 5 on
     `NumberDataPoint`, 8 on `HistogramDataPoint`) with the identifiers in **hex**,
     per §Decision 6; the Prometheus connector cannot carry one at all in the text
     exposition format and adds it to its enumerated losses (ADR 0044 §Decision 8).
- **Batching / a span processor.** A `SpanSink` receives one span at a time
  precisely so a batching processor is a sink that buffers and forwards, where its
  cost is visible. The current `Recorder` accumulates and hands out on `Collect`,
  which is the pull shape a `scheduler` job wants; a push-with-timeout batcher is
  additive and brings a benchmark with it.
- **Variable-rate / adaptive sampling.** A rate that changes at runtime in response
  to load. It needs a feedback signal, a decision cadence and an answer to "what
  does the rate mean for a trace already in flight" — a whole policy surface, and
  the fixed `Ratio` is what a deployment can reason about today.
- **The three-valued `SamplingDecision`** (RECORD_ONLY). §Decision 4.
- **A gRPC or messaging propagator.** `Carrier` is already the seam; what is
  missing is the semantic-convention attribute set for each transport, not the
  plumbing.
- **`SchemaURL`** on Resource and Scope, and scope-level attributes — inherited
  verbatim from ADR 0044 §Deferred, for the same reason: optional, unproduced, and
  an always-empty field is a placeholder (rule 5).
- **OTLP/protobuf, gzip request compression, and `OTEL_EXPORTER_OTLP_*`
  environment configuration** — all three deferred by ADR 0048 for reasons that are
  unchanged here, and all three would be one implementation shared by both signals.
- **A span limit on attributes / events / links**, and the
  `droppedAttributesCount` family of fields that reports one. Nothing here drops
  them, so the fields are absent rather than always-zero.

## Why not

- **Import `go.opentelemetry.io/otel/trace` (+ `otel/sdk/trace`, + the OTLP
  exporter).** Rejected for the reason ADR 0044 §Decision 1 rejected importing the
  metrics model: it introduces `x/sys`, banned SDK-wide (ADR 0022 / ADR 0034), plus
  a protobuf runtime and a release cadence this repo does not control — in exchange
  for a data model that is a shape and a JSON document under 700 lines of
  declarations. What interoperability needs is the wire.
- **Twin the attribute model in `core/trace` for a clean import graph.** Rejected:
  §Decision 2. Two types that encode identically are invisible in review and
  surface as a conversion at the exact boundary exemplars have to cross.
- **Move `AttrValue` / `ResourceValue` to a shared `core/otel` in this change.**
  Considered and deferred: it moves types published through a `pkg/v1/metrics`
  alias, spending the ADR 0040 v0 licence on a rename with no behavioural content,
  while several domains are landing on the same tree. Recorded in §Decision 2 with
  its shape, so it is a refactor somebody can pick up rather than a gap.
- **Return an error from `Extract`.** Rejected: §4.3 prescribes exactly one
  response and the header is attacker-controlled. An error invites a caller to fail
  a request over a stranger's typo.
- **Consult the sampler per span.** Rejected: §Decision 4. It produces traces with
  holes, and the holes are invisible at the process that makes them.
- **Ship `Ratio(0)` as "sample nothing".** Rejected: §Decision 5. It is the same
  value as "unconfigured", and honouring it turns a misconfiguration into silence.
- **Return `nil` from `Start` for an unsampled span.** Rejected twice over: it
  would stop the decision propagating (turning one dropped trace into N kept ones)
  and would put `if span != nil` at every instrumentation site, where the one place
  it was forgotten panics on the cheap path.
- **Put `RecordError` on the `Span` port.** Rejected by ADR 0039: it freezes a
  judgement about the caller's error model into an interface every downstream
  implementer must satisfy, to save one function call.
- **Register the OTLP/HTTP exporter with a default endpoint** (`localhost:4318`).
  Rejected: §Decision 6, and one step past ADR 0030 — an import would arm a network
  client at an address the caller never named.
- **Retry inside `Export`.** Rejected for ADR 0048's reasons, unchanged:
  `resilience` exists, a hidden backoff cannot be tuned or cancelled by the caller
  who owns the export loop, and `Export` has no context to cancel it with.
- **Name the server span `{method} {path}`.** Rejected: §Decision 7. It is the
  cardinality mistake that makes a tracing bill a story, and it is invisible until
  the bill arrives.
- **Forward `Flush` and `Hijack` from the status recorder.** Rejected: §Decision 7.
  It would make the wrapper lie about capability, which is the ADR 0047 defect
  re-introduced one layer up.
- **Emit an all-zero `parentSpanId` for a root span.** Rejected: it is the
  identifier W3C Trace Context declares invalid, and the schema's own spelling for
  "no parent" is an absent field.
- **Base64 the identifiers, as the generic protobuf-JSON mapping says.** Rejected
  by the specification itself, which overrides the mapping by name for exactly
  these two fields. It is the trap a generated encoder falls into, so it has its
  own test.

## References

- W3C **Trace Context** (Recommendation) — <https://www.w3.org/TR/trace-context/>
  — §3.2 `traceparent` (§3.2.2.1 version, §3.2.2.3 trace-id, §3.2.2.4 parent-id,
  §3.2.2.5 trace-flags, §3.2.4 versioning), §3.3 `tracestate` (§3.3.1.1–§3.3.1.5),
  §3.5 mutating, §4.2–§4.3 processing model
- OpenTelemetry **trace** specification (API + SDK: span kinds, status, sampling) —
  <https://opentelemetry.io/docs/specs/otel/trace/api/>
- OpenTelemetry **HTTP semantic conventions** (the attribute keys and the
  4xx/5xx asymmetry) — <https://opentelemetry.io/docs/specs/semconv/http/>
- OTLP specification (§JSON Protobuf Encoding, §OTLP/HTTP, §Retryable Response
  Codes) — <https://opentelemetry.io/docs/specs/otlp/>
- `opentelemetry/proto/trace/v1/trace.proto` — `TracesData`, `ResourceSpans`,
  `ScopeSpans`, `Span`, `Span.Event`, `Span.Link`, `Status`, `SpanFlags`
- `opentelemetry/proto/common/v1/common.proto` — `AnyValue`, `KeyValue`,
  `InstrumentationScope`
- `opentelemetry/proto/resource/v1/resource.proto` — `Resource`
- `opentelemetry/proto/collector/trace/v1/trace_service.proto` —
  `ExportTraceServiceRequest`, `ExportTraceServiceResponse`,
  `ExportTracePartialSuccess`
- RFC 7230 §3.2.2 (field order — the two-`traceparent` case) and §3.2.4 (field
  value whitespace — the OWS leniency)
- Impl: `internal/core/trace/`, `internal/service/trace/`, `pkg/v1/trace/`
- Rationale in place: `internal/core/trace/CLAUDE.md`,
  `internal/service/trace/CLAUDE.md`, `pkg/v1/trace/CLAUDE.md`
- ADR 0044 (the model + the OTel-without-OTel argument), ADR 0048 (the wire and the
  encoder/emitter split), ADR 0029 (the middleware type), ADR 0047 (the hijack
  defect), ADR 0031 (clamp vs refuse), ADR 0039 (frozen ports), ADR 0041 (func
  ports), ADR 0035 (code ranges)
