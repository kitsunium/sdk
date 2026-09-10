# internal/core/trace/

## Purpose

Declares the SDK's **distributed-tracing port**: the `Tracer`/`Span` pair an
application instruments against, the immutable `SpanContextValue` that travels
between processes, the OpenTelemetry trace data model, and the W3C Trace Context
propagation format. The 17th core sibling, admitted by **ADR 0051**.

Like `metrics` since ADR 0044, it speaks the OpenTelemetry model and imports
**none** of OpenTelemetry's code. OTel is a published specification; this SDK
implements it from the document. Interoperability is a property of the WIRE, not
of the import graph.

Concrete implementations — the tracer, the samplers, the recorder, the OTLP/JSON
encoder, the HTTP middlewares — live in `internal/service/trace`.

Code range: `0.2.20.*` (ADR 0051).

## Contents

| File | Surface |
|---|---|
| `trace.go` | package doc + `Tracer` (1 method, FROZEN) + `Span` (5 methods, FROZEN) |
| `span_params.go` | `SpanParams` — the facts a span is born with; the zero value is an INTERNAL span starting now |
| `identifier.go` | `TraceID` / `SpanID` / `TraceFlags` + `ParseTraceID` / `ParseSpanID` + `FlagSampled` + `Sanitized` |
| `span_context.go` | `SpanContextValue` — TraceID, SpanID, Flags, State, Remote; `IsValid` / `IsSampled` / `WithState` |
| `traceparent.go` | `ParseTraceParent` / `FormatTraceParent` + `TraceParentHeader` / `TraceStateHeader` / `TraceParentLen` / `VersionSupported` |
| `trace_state.go` | `TraceStateValue` — ordered, immutable vendor list; `Get` / `Insert` / `Delete` / `Len` / `String` + the three grammar limits |
| `carrier.go` | `Carrier` (2 methods, FROZEN) + `Inject` / `Extract` |
| `context.go` | `ContextWithSpanContext` / `SpanContextFromContext` |
| `span_kind.go` | `SpanKind` + the five values + `Resolved` |
| `status_value.go` | `StatusValue` + `StatusCode` (`Unset`/`OK`/`Error`) + `Resolved` / `IsUnset` |
| `event_value.go` | `EventValue` + the `exception.*` convention constants |
| `link_value.go` | `LinkValue` — a whole `SpanContextValue` plus attributes |
| `span_value.go` | `SpanValue` — one FINISHED span; `Duration` / `IsRoot` |
| `spans_value.go` | `SpansValue` — Resource + Scope + spans, the exportable payload |
| `sampler.go` | `Sampler` and `SpanSink` — FUNC ports (ADR 0041) |
| `sampling_params.go` | `SamplingParams` — what a Sampler sees |
| `scope.go` | `DefaultScopeName` + `NormalizeScope` |
| `attrs.go` | the ALIASES of `core/metrics`' attribute / Resource / Scope model, and why |
| `exporter.go` | `SpanExporter` + `ExporterName` + registry (`RegisterExporter` / `LookupExporter` / `AvailableExporters` / `Export`) |
| `codes.go` | `Code*` constants — range 0.2.20.* |
| `errors.go` | `InvalidTraceParent` / `InvalidTraceState` / `UnknownExporter` / `ExportFailed` / `DuplicateRegistration` / `InvalidSpanName` |

## The one rule everything else follows from

**The sampling decision is taken ONCE, at the root, and travels in the `sampled`
bit of the traceparent.**

Everything odd-looking in this package is downstream of it:

- `Sampler` is documented as consulted for root spans only — that is a property
  of the `Tracer`, stated here because this is where a reader looks for it.
- An unsampled span is a no-op that still carries a valid `SpanContextValue`,
  so `Inject` still writes a header, with the bit CLEAR.
- `SpanContextValue.IsSampled` is the ONLY sampling question anything downstream
  asks.

Deciding per span produces a trace with holes in the middle. A span whose parent
was dropped becomes an orphan the backend renders as its own root, so one request
appears as several unrelated ones and the latency of the whole is unrecoverable —
and nothing looks wrong at the process that caused it.

## Why the attribute model is `core/metrics`' — Do NOT twin it

`AttrValue`, `ResourceValue` and `ScopeValue` are **type aliases**, so
`pkg/v1/trace.Attr` and `pkg/v1/metrics.Attr` are one type.

They are not metrics concepts. `AttrValue` is `common/v1.KeyValue`/`AnyValue`,
`ResourceValue` is `resource/v1.Resource`, `ScopeValue` is
`common/v1.InstrumentationScope` — all three live in the SHARED protos precisely
because every signal uses them. They sit in `core/metrics` only because metrics
was the first signal this SDK implemented.

A twin costs three things, and none of them is hypothetical:

1. `metrics.String("k","v")` and `trace.String("k","v")` become incompatible
   values spelling one fact, so every dual-signal consumer writes a converter.
2. A process carries **two Resources that can disagree**, and `service.name` is
   the key a backend correlates a trace with a metric on.
3. **Exemplars** — ADR 0044 deferred them waiting on exactly this domain — attach
   a trace id to a metric data point. One model makes that a field; two make it a
   conversion at the boundary the feature exists to cross.

`DefaultScopeName` is the exception and it is NOT shared: a scope names the
library that produced THIS signal, so a span batch stamped `…/pkg/v1/metrics`
would be a lie. `NormalizeScope` is this package's own, guarded by
`TestScopeDefaultNamesTheTracePackage`.

The extraction that would make the graph read forwards — a shared
`internal/core/otel` under both — is recorded in ADR 0051 §Decision 2 with the
reason it is not done yet. Do not do it piecemeal.

## W3C Trace Context — the refusals that look arbitrary

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

`TraceStateValue` is an ordered slice and not a map, because §3.5 makes the order
meaning: leftmost is the system that touched the trace most recently. The
grammar's own numbers are the limits (32 members, 256-char keys and values,
241/14 for `tenant@system`) — there is no SDK-invented ceiling.

**One leniency, and it is the only one**: OWS is stripped at the list's edges,
where the `list` rule grants no OWS slot, because RFC 7230 §3.2.4 already
normalises a field value's surrounding whitespace. The `nblk-chr` rule is
enforced in `Insert` instead. Documented in place and in
`TestTraceStateAbsorbsEdgeWhitespaceButInsertRefusesIt`.

## Do NOT

- **Do NOT add a method to `Tracer`, `Span` or `Carrier`.** All three are
  published through `pkg/v1/trace` aliases and Go interfaces are structural, so
  widening breaks every downstream double at compile time with no deprecation
  window (ADR 0039). `Carrier` is two methods on purpose: it is exactly
  `http.Header`'s `Get`/`Set` pair, which is what lets `Inject(ctx, req.Header)`
  compile with no adapter. A test holds that assignment.
- **Do NOT import `net/http` here.** `Carrier` exists so this package does not
  have to. An HTTP opinion in the contract would exclude a message queue and a
  gRPC metadata map.
- **Do NOT add `RecordError` to `Span`.** What an error's TYPE is, is a judgement
  about the caller's error model. It is a helper in `internal/service/trace`, and
  that is the shape that does not freeze a decision into a port.
- **Do NOT emit an all-zero identifier.** It is the specification's own invalid
  value; `Inject` writes nothing for an invalid context, and the OTLP encoder
  refuses one outright.
- **Do NOT re-derive the sampling decision anywhere below the root.**
- **Do NOT clear undefined trace-flags bits on receipt.** See above.

## Verification

| Command | Expected |
|---|---|
| `GOWORK=off go test ./trace/...` (from `internal/core`) | green |
| `bazel test //internal/core/trace:trace_test` | green |
| `TestParseTraceParentRefusals` | every W3C refusal, each naming its section |
| `TestParseTraceParentIsForwardCompatible` | §3.2.4, including the undashed-tail case |
| `TestFormatTraceParentMasksUndefinedFlagBits` | mask on output, keep on input |
| `TestExtractRestartsTheTraceOnAMalformedParent` / `…KeepsAValidParentDespiteAnUnreadableTraceState` | both halves of §4.3 |
| `TestAttributesAreTheSameTypeAsMetrics` | the alias, as a value fact |
| `TestScopeDefaultNamesTheTracePackage` | the one thing that is NOT shared |
| `TestHTTPHeaderIsACarrierWithNoAdapter` | the ADR 0039 freeze on `Carrier` |
