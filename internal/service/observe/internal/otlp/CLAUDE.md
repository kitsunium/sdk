<!-- updated: 2026-10-05T00:00:00Z -->
# internal/service/observe/internal/otlp/

## Purpose

The OTLP machinery the **metrics** and **trace** signals share, written once:
the proto3-JSON scalar encodings, the `common/v1` and `resource/v1` messages
every payload carries, the single-document marshal, the newline-delimited stream
a writer-bound exporter emits, and the **OTLP/HTTP sender** — endpoint refusal,
the default client and its own connection pool, the bounded body reads, the
drain that recycles the connection, and the classification of a collector's
answer into three verdicts.

It was written twice — `internal/service/observe/metrics` (ADR 0048) and
`internal/service/observe/trace` (ADR 0051) — as near-copies: twelve identical helpers,
the same response decoder, the same configuration, the same attribute encoder.
The copies differed in exactly three things, and those three are what this
package does **not** own:

1. **the error codes** — metrics `0.3.45.7`–`.10`, trace `0.3.50.5`–`.8`;
2. **the wording** of each refusal's public and private message;
3. **the names** a partial success is counted under — the errs field
   (`rejected_data_points` / `rejected_spans`) and the response member
   (`rejectedDataPoints` / `rejectedSpans`).

A signal hands them in as a `SignalSpec`, the error-code injection seam, and every
error leaves this package under the calling signal's dotted-quad code and in its
words — byte for byte what that signal returned before the transport was shared.
Both signals' existing suites, which assert codes and wording, run unchanged
against it.

Code range: **none** — it declares no code; every sentinel and wrap it emits is
the caller's.

Internal to `internal/service/observe` (Go's `internal/` rule) on purpose:
nothing above the service layer may reach it, and nothing but the two signals
needs to. Since ADR 0155 it sits in the observe family's own `internal/`, so the
rule names the packages that may import it — the family's engines — instead of
the whole service layer. The
public names a caller types — `OTLPHTTPConfig`, `NewOTLPHTTPExporter`,
`OTLPRetryable`, the path constants, `DefaultOTLPTimeout`,
`DefaultOTLPMaxResponseBytes` — stay declared per signal, as thin wrappers.

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `otlp.go` | `DefaultTimeout` / `DefaultMaxResponseBytes` / `DocumentTerminator` |
| `scalar.go` | `Int64` / `Uint64` (decimal strings) and `Double` (a number, or `"NaN"` / `"Infinity"` / `"-Infinity"`) with their `MarshalJSON`, and the unexported renderings their `AppendJSON` share |
| `append.go` | the appenders both signals render their trees with: `AppendString` (encoding/json's string escaping with HTML escaping off), `AppendKey` (a member name, its comma decided by the `{` before it), `AppendBool`, `AppendNull`, `AppendKeyValues`, and `AppendJSON` on `Int64` / `Uint64` / `Double` / `AnyValue` / `KeyValue` / `ResourceMessage` / `ScopeMessage` — `Marshal`'s bytes, without reflection and without a slice per scalar |
| `key_value.go` | `KeyValue` + `Attrs` + `KeyValueOf` — the shared attribute model (`internal/core/observe/otel`) as `common.v1.KeyValue` |
| `any_value.go` | `AnyValue` — the four scalar cases of the oneof, presence kept at the zero |
| `resource_message.go` | `ResourceMessage` + `ResourceOf` |
| `scope_message.go` | `ScopeMessage` + `ScopeOf` |
| `marshal.go` | `Marshal` (one document, HTML escaping off, no trailing newline, a rendering fault under the caller's wrap) — the REFERENCE the appenders are tested against, no signal exports through it any more — + `UnixNano` (the unset instant is 0) |
| `stream.go` | `Stream` + `NewStream` + `Emit` — one terminated document per write, a writer fault under the caller's wrap |
| `signal_spec.go` | `SignalSpec` — the per-signal vocabulary: four sentinels, three wraps of a foreign cause, the rejected field key, the response decoder |
| `http_config.go` | `HTTPConfig` — each field documented once; each signal publishes it as a DEFINED type of its own |
| `sender.go` | `Sender` + `NewSender` + `Post` / `PostContext` — the request carries the caller's context, so a context done before the collector answers ends the round trip with the transient verdict (#479); `Post` is `PostContext` under `context.Background()` —, and everything behind them: `checkEndpoint`, `newClient`, `newTransport`, `closeResponse`, `swallowTeardown`, `classify`, `isRetryableStatus`, `retryAfterSeconds`, `partialSuccessOf`; the `retry_after_seconds` field is the key `app/resilience.RetryAfter` reads, pinned by `TestTheRetryPolicyReadsTheCollectorsDelay` (#476) |
| `response.go` | `LenientInt64` (number OR decimal string) + `RejectedCounter` + `DecodeRejected` |
| `wire_external_test.go` | scalars, attributes, messages, the marshal, the caller's code on a fault, the unset instant, the stream under concurrency |
| `sender_external_test.go` | the three verdicts for two stand-in signals, every success shape, every endpoint refusal, the transport fault, header precedence, the defaults |
| `append_external_test.go` | the appenders against `Marshal`, encoding/json the oracle: every string escape class (and a fuzz target seeded with them), the scalars at their extremes, every message and omission, the member comma |
| `marshal_bench_test.go` / `BENCH.md` | the cost of one document through `Marshal` and through the appenders, and a pooled-buffer CONTROL kept beside them — measured not to pay |

## Why each signal still keeps four small things

- **`OTLPHTTPConfig` is a defined type, not an alias.** `type OTLPHTTPConfig
  otlp.HTTPConfig` keeps `metrics.OTLPHTTPConfig` and `trace.OTLPHTTPConfig` two
  types a caller cannot hand to the wrong signal's constructor, while the fields
  are declared and documented once here. The constructor converts the pointer
  (`(*otlp.HTTPConfig)(&cfg)`), which the identical underlying types make free.
- **The partial-success message.** The two messages differ in one member name.
  Each signal declares a three-line struct with its own static json tag and a
  `RejectedCount` method; `DecodeRejected` decodes into it through an interface
  holding the pointer, so encoding/json matches members exactly as it did when
  each signal decoded its own response type — a generic or a `map` would each
  have changed a corner case.
- **`OTLPRetryable`.** One line per signal, because the transient code is the
  signal's.
- **The payload tree and its encoder.** `ResourceMetrics` and `ResourceSpans`
  share nothing below the Resource and Scope messages, and the refusals only an
  encoder can raise (an unresolved temporality, an unended span) are its own.

## Conventions

- **Field order is the schema's field-number order**, in every message here and
  in each signal's tree, so the expected bytes in the tests are checkable
  against the `.proto` field by field.
- **Presence is meaning.** A oneof member and an `optional` field are emitted at
  their zero (`{"boolValue":false}`); an empty repeated field is omitted.
- **The sender never retries.** It classifies; each signal's `OTLPRetryable` is
  the predicate `resilience.RetryConfig.Retryable` takes (ADR 0048).
- **Remote-controlled text never reaches an error.** The collector's
  `errorMessage` is decoded and never read; the endpoint is never echoed; the
  header values are written and never read back.
- **A `SignalSpec` is a package-level value** of its signal, handed by pointer;
  every field is required.

## Do NOT

- **Declare an error code here**, or default a `SignalSpec` field. A verdict
  without the caller's code would be a verdict under nobody's.
- **Retry inside `Post` or `PostContext`.** The context `PostContext` takes
  bounds ONE round trip; a hidden loop would spend that bound on attempts the
  caller cannot see (ADR 0048 §Decision, ADR 0051 — unchanged).
- **Follow a redirect, or share `http.DefaultTransport` in the default client.**
  `newClient` and `newTransport` say why: CWE-918, and a pool anything in the
  process can empty under a response that already arrived.
- **Read a body unbounded** — the verdict read and the drain are both bounded,
  the drain always by `DefaultMaxResponseBytes`.
- **Pool the marshal buffer.** Measured (`BENCH.md`): `encoding/json` already
  renders into a pooled `encodeState` and writes the document once, so a fresh
  `bytes.Buffer` costs one exact-size allocation that IS the returned
  document; a pool saves two header allocations at small sizes and costs
  5–9 % more bytes at 1 000 points, because the returned document must be
  copied out. The cost of an export through `Marshal` was the tree and the
  scalars' own `MarshalJSON` — ~9 000 allocations at 1 000 points, of which the
  buffer is one. The `_PooledControl` benchmarks keep the question re-askable.
  The signals now render with the appenders into one buffer sized near the
  document: the 4 000 scalar slices are gone and the document is ONE allocation
  (`BenchmarkAppend_1000`); what remains is the tree's.
- **Let an appender drift from `Marshal`.** `encoding/json` is the oracle:
  `AppendString` copies its escaping (with HTML escaping off) — a raw U+FFFD for
  an invalid byte, U+2028 and U+2029 escaped, the five short escapes — and
  `append_external_test.go` plus each signal's `otlp_append_internal_test.go`
  hold every appender to it. A rule changed in one place and not the other
  ships a payload a collector parses differently from every other producer's.
- **Use `encoding/json/v2` (`MarshalJSONTo`) instead.** It is still behind
  `GOEXPERIMENT=jsonv2` in Go 1.27.3 (`encoding/json/v2` carries
  `//go:build goexperiment.jsonv2`), which no build of the SDK sets, and its
  output details (omission, escaping) differ from v1's.
- **Move a signal's payload tree here.** What is shared is what both signals
  emit identically; a metric point and a span are not that.
- **Register anything.** The OTLP/HTTP exporter is never registered (ADR 0030,
  ADR 0051 §Decision 6); this package is not reachable from an import outside
  `internal/service/observe` anyway.

## Verification

```
cd internal/service && GOWORK=off go test -race ./observe/internal/otlp/ ./observe/metrics/ ./observe/trace/
bazel test --config=race //internal/service/observe/internal/otlp:otlp_test
```
