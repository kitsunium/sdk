# ADR 0062 — a log line names the span it came from, and the bridge lives at the top layer

- **Status**: Accepted
- **Date**: 2026-09-10
- **Deciders**: SDK maintainers
- **Amended by**: [ADR 0070](0070-logger-reserves-the-correlation-keys.md) — a caller's attribute named `trace_id` or `span_id` is renamed rather than written beside the field
- **Related**: [ADR 0051](0051-sdk-trace-domain.md) (`trace`, whose span context this reads), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a published port grows by siblings), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (a published shape while v0), [ADR 0004](0004-sdk-bazel-build-system.md) (layer visibility), [ADR 0030](0030-stdout-is-a-protocol-channel.md) (a zero value must not be the dangerous one)

## Context

ADR 0051 landed the third pillar. An operator can now open a trace in a backend
and read a request end to end — and then cannot find a single log line that
belongs to it. The two pillars are in the same binary, in the same request, on
the same `context.Context`, and they do not know about each other.

The fix is one sentence — *put the span's identifiers on the log record* — and
four decisions, none of which is obvious.

**The context is already there.** The first assumption to check, and the ticket
said to check it: `core/logger.Logger.Log` has taken a `context.Context` as its
first parameter since it was written, and so have `Handler.Enabled` and
`Handler.Handle`. Nothing has to be widened, nothing published breaks, and
ADR 0039's prohibition on growing a published interface never comes into play.
The context is not the problem. **Reaching it from the formatter is.**

**The formatter has no context.** `Encoder.Append(dst []byte, groups []string,
r RecordEvent) []byte` receives the record and nothing else. Whatever the
encoder is going to print has to be ON the record by the time it is called.

**The cost is a hard budget.** The logger's steady-state cost is exactly one
heap allocation per emit — the attrs clone the handler makes on every record —
claimed in the root `CLAUDE.md`, measured in `pkg/v1/logger/BENCH.md`, and
guarded in the race-off allocation lane. `TraceID.String()` is a Go string, and
a Go string is a heap allocation. Rendering `trace_id` the obvious way doubles
the logger's allocation budget on every line a service emits.

**And the two domains are siblings.** `internal/service/logger` and
`internal/service/trace` are the same layer; neither may import the other. That
much is enforced by Bazel visibility. What is NOT enforced, and is the actual
question, is whether the *logging* domain should depend on the *tracing* domain
at all, at any layer.

## Decision

1. **The identity is a FIELD of the record, not two attributes.**
   `RecordEvent` grows one field, `TraceContext TraceContextValue`, carrying the
   16-byte trace id and the 8-byte span id and nothing else — no flags, no
   tracestate. Two reasons, and either alone would settle it. The encoder has
   no context, so an attribute injected at emit time is the only alternative
   shape and it does not reach the formatter any other way. And an attribute
   could not satisfy the specification: OpenTelemetry requires `trace_id` and
   `span_id` to be **top-level keys** of the log object
   (`specification/compatibility/logging_trace_context.md`), while an attribute
   under `WithGroup("http")` renders as `http.trace_id`, which no ingestion
   pipeline recognises. A field cannot be prefixed by a group; an attribute
   always can be. There is a named test for exactly that.

2. **The field names, the alphabet and the widths come from the specification,
   not from taste.** `trace_id` and `span_id`, lowercase hex, 32 and 16 digits
   — "use the field names `trace_id` for the TraceId, `span_id` for the SpanId
   … Trace IDs and span IDs must be lowercase and hex-encoded"
   (`specification/compatibility/logging_trace_context.md`). This is the
   non-OTLP branch of the specification, which is the branch a text line and a
   JSON line are on. `trace_flags` is defined in the same sentence and is
   deliberately **not** emitted: it is the sampling decision, which is a
   property of the trace and not of the line, and nothing an operator does with
   a log line needs it. Adding it later is additive.

3. **An absent span emits nothing at all.** Not `trace_id=""`, not
   `trace_id=000…0`. An all-zero trace-id is invalid under W3C Trace Context
   §3.2.2.3 and an all-zero span-id under §3.2.2.4, so either spelling puts a
   field no query can join on onto **every line a service logs outside a
   request** — which is most of them, and which would make `trace_id` useless
   as a filter. `TraceContextValue.IsValid()` is the single gate all three
   formatters consult, and it requires BOTH identifiers: the data model asks
   that a SpanId never travel without its TraceId
   (`specification/logs/data-model.md`, Trace Context Fields), and a trace id
   with no span id names no span a backend can join. The absent case is tested
   at four levels — the value, both encoders, and the whole `NewText` pipeline —
   and the assertion is that the KEY is absent, not that the value is empty.

4. **The correlation costs zero additional allocations, and the identifier is
   never a Go string.** The extraction returns a value type read out of the
   context (`0 B/op`, measured), and the identifiers reach the output as
   `hex.Encode` into a stack array appended straight into the buffer the
   handler already borrowed from the pool. `AppendTraceIDHex` / `AppendSpanIDHex`
   live on the value in `core/logger` rather than three times over in
   `service/logger`, because three formatters have to agree on those bytes
   exactly. Measured: **1 alloc/op with a span, 1 alloc/op without one**, on
   all three emission paths. See "Consequences".

5. **The bridge lives in `pkg/v1/logger`, and the port that lets it live there
   is in `core/logger`.** This is the half of the work that is placement rather
   than code:

   - `internal/core/logger` declares `TraceContextValue` (its own two arrays,
     not the trace domain's types) and `TraceContextSource`, a FUNC port that
     reads one off a `context.Context`. The package stays **stdlib-only**.
   - `internal/service/logger` takes a `TraceContextSource` at construction
     (`NewWithTraceContext`) and calls it once per emitted record. It never
     learns which propagation carries the span, so it keeps **zero edges** to
     any other service-layer package.
   - `pkg/v1/logger` binds the port to `internal/core/trace.SpanContextFromContext`
     and wires it into every constructor it offers. It is the top layer and is
     already allowed to know both domains; it is the ONLY file in the SDK where
     logging and tracing meet.

   The port is a func type, so ADR 0039 is satisfied structurally: a func
   cannot grow a method, so publishing it can never break a downstream
   implementation.

6. **Correlation is ON by default, and there is no knob to turn it off.** Every
   Logger built through `pkg/v1/logger` is correlated. This is the opposite call
   from ADR 0030's caution about zero values, and deliberately: the dangerous
   default there was writing to a protocol channel, whereas here the "dangerous"
   outcome is a ~30 ns context read that emits nothing when there is no span.
   A knob would mean the feature is present, documented, and silently off in
   the deployment that needs it — which is how an operator ends up back where
   this ADR started.

## Consequences

- `RecordEvent` grows a field, and `pkg/v1/logger.Record` is an alias of it, so
  a published shape changed. It is **additive** — a keyed literal, which is what
  the SDK writes, still compiles, and the zero value is the correct "no trace
  here" — so ADR 0040's v0 licence is not spent on it. Said out loud rather than
  left silent. An unkeyed `RecordEvent{…}` literal in downstream code would
  break; there is none in this repository.
- A `Sink` sees the identity on the record, not only in the formatted bytes, so
  a structured transport (syslog, CloudWatch, a DB writer) can put it in its own
  column instead of parsing it back out of a line. Pinned by a `MemorySink` test.
- `pkg/v1/logger` gains a compile-time edge to `internal/core/trace` (and
  through it `core/metrics`, `kernel/snapshot`). No external dependency, no
  runtime cost, and it stops at `core` — the OTLP exporters and `net/http` that
  live in `internal/service/trace` are NOT pulled in. `internal/core/logger` and
  `internal/service/logger` gain nothing.
- Measured on the reference box (`pkg/v1/logger/BENCH.md`): the extraction is
  ~32 ns and 0 allocations whether a span is in scope or not, and the emit path
  costs **1 alloc/op with and without a span**, unchanged, on the variadic, the
  slice-overload and the chainable paths.
- The guard is stricter than the one it joins.
  `TestV116BuildSendAllocatesOnePerEmit` asserts `>= 1` — it exists to refute a
  "zero-allocation" claim, so it is one-sided and would not have noticed a
  regression upward. `TestT34TraceCorrelationAddsNoAllocation` asserts
  **exactly 1**, for both the in-span and out-of-span case, on all three paths.
  Both run in the same lane; `tools/alloc-lane-targets.txt` already covers
  `//pkg/v1/logger:logger_test`, so root `CLAUDE.md` rule 12 needs no new entry.

## Why not

- **Why not `WithSpan(ctx, lg) Logger`, binding the ids once per request?** It
  is cheaper still — the hex is formatted once per span instead of once per
  line — and it was rejected because it is a line the caller has to remember to
  write, in every handler, forever. The operator's complaint is not "correlation
  is expensive", it is "correlation is absent", and an opt-in call reproduces
  the absence in every code path someone forgot. The automatic path was made
  free instead.
- **Why not have `core/trace` register its extractor into `core/logger` from an
  `init()`?** It would give automatic correlation with no import edge from
  logger to trace at all, and it is refused: it inverts the dependency, it makes
  behaviour depend on link-time reachability (whether the trace package happens
  to be in the binary), and it is exactly the "arming something from an import"
  shape ADR 0048 already refused for the OTLP emitter. A dependency you can see
  is better than one you cannot.
- **Why not reuse `core/trace.SpanContextValue` as the record's field type?**
  Because it would put `core/trace` — and `core/metrics` behind it — in front of
  every consumer who wants a line on stderr, and because a log record does not
  want the rest of it: not the flags, not the tracestate, not `Remote`. The two
  16- and 8-byte arrays are a deliberate re-declaration, and the conversion in
  `pkg/v1/logger` is also the compile-time proof that the two domains agree on
  the widths — a mismatch would not build.
- **Why not nest them under a `trace` object?** Because the specification says
  top-level keys, and the maintainer's instruction was OTel in the SDK rather
  than a second format that resembles it.
- **Why no error code, and no new `PP` range?** Nothing here can fail. The
  extraction returns a value, an absent span is not a fault, and a malformed one
  was already refused upstream by `trace.Extract`. A domain that cannot fail
  does not get a code block.

## References

- OpenTelemetry, `specification/compatibility/logging_trace_context.md` — field
  names `trace_id` / `span_id` / `trace_flags`, lowercase hex, **top-level keys**
  of the JSON log object.
- OpenTelemetry, `specification/logs/data-model.md`, *Trace Context Fields* —
  the fields are optional; if a SpanId is provided the corresponding TraceId
  should also be included.
- W3C Trace Context, §3.2.2.3 (all-zero trace-id is invalid) and §3.2.2.4
  (all-zero span-id is invalid).
- `internal/core/logger/trace_context.go`, `internal/core/logger/record.go`,
  `internal/service/logger/logger.go`, `internal/service/logger/encoder/{text,json}.go`,
  `internal/service/logger/text_handler.go`, `pkg/v1/logger/tracecontext.go`.
- `pkg/v1/logger/BENCH.md` — the numbers behind decision 4.
