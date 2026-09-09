# ADR 0048 — OTLP/JSON: the native wire, written from the document

- **Status**: Accepted
- **Date**: 2026-09-09
- **Deciders**: SDK maintainers
- **Related**: [ADR 0044](0044-metrics-adopts-the-otel-data-model.md) (the data model this encodes, §Decision 9 in particular), [ADR 0027](0027-sdk-metrics-domain.md) (the domain), [ADR 0030](0030-stdout-is-a-protocol-channel.md) (what a registered exporter may write to), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (clamp vs refuse), [ADR 0026](0026-sdk-resilience-domain.md) (the retry policy this does not reimplement), [ADR 0018](0018-sdk-cross-platform-portability.md) (stdlib-only)
- **Closes**: ADR 0044 §Deferred — "The OTLP exporter itself"

## Context

ADR 0044 reshaped `metrics` onto the OpenTelemetry data model and then stopped
one step short, on purpose: it shipped a snapshot that an OTLP encoder could
walk *without regrouping or reconstruction*, and left the encoder for the next
change. This is that change.

Until now the domain had one wire and it was the wrong one. The Prometheus text
exposition connector is kept because a Prometheus deployment is a real
destination, but ADR 0044 §Decision 8 enumerates what it drops on the floor:
temporality (refused outright), the attribute's type, the `Resource`, the
`InstrumentationScope`, and every OTel-conventional dotted key. A model that
carries all five and a wire that carries none of them is a model nobody can
observe. **OTLP is the wire the model was adopted for**, and it loses nothing.

The constraint is the one that produced the model in the first place: nothing
imports `go.opentelemetry.io`, and nothing imports a protobuf runtime. OTLP is a
specification with a published schema. `encoding/json` and `net/http` are enough
to speak it, exactly as `encoding/json` and `net/http` were enough for RFC 7517,
the JWS Compact Serialization, the Prometheus exposition format and a five-field
POSIX cron.

The specification does not make that free. OTLP/JSON is protobuf-JSON, and
protobuf-JSON is not "the obvious JSON for this struct". Four of its rules are
invisible until a receiver rejects the payload — or worse, accepts a wrong one.

## Decision

### 1. Two surfaces, and the boundary is the byte slice.

`EncodeOTLPJSON(SnapshotValue) ([]byte, error)` is the **encoder**: a pure
function returning exactly the bytes of one `ExportMetricsServiceRequest`. It
touches no socket, no writer, no clock. `NewOTLPHTTPExporter(name, cfg)` is the
**emitter**: it calls that function and adds transport, nothing else.

The split is not tidiness. An encoding defect and a network defect have nothing
in common — different reproduction, different evidence, different fix — and a
single `Export` that did both would make every OTLP question start with "is the
collector up?". With the split, the schema conformance test needs no server and
the transport test needs no schema.

It also gives a caller three real entry points instead of one: the bytes
(`EncodeOTLPJSON`), a stream (`NewOTLPJSONExporter(name, w)`), or a collector
(`NewOTLPHTTPExporter`). The first is what a queue, a file or a compressor
wants; the SDK does not have to guess which.

One deliberate difference between them: the **writer**-bound exporter terminates
each document with a newline, so a stream of exports is NDJSON and a terminal or
a log file is readable. The **HTTP body** is the document alone, because a body
is one message. `EncodeOTLPJSON` returns the body form.

### 2. The four protobuf-JSON rules, verified against the document.

Each of these was read out of the specification and the schema rather than
assumed, and each has a test whose expected bytes were written by hand.

| Rule | Where it says so | What breaks without it |
|---|---|---|
| Field names are **lowerCamelCase** (`dataPoints`, `startTimeUnixNano`) | OTLP §JSON Protobuf Encoding, naming the snake_case form as invalid | A conforming receiver ignores every unknown field, so a snake_case payload arrives as an empty request and is accepted |
| 64-bit integers are **decimal strings**, and either form is accepted on decode | same section, quoting the protobuf rule | A JSON number is a double in most parsers: a counter past 2⁵³ silently loses its low bits |
| Enums are **integers**, never names | same section: "the enum name strings MUST NOT be used" — this is OTLP overriding generic proto3 JSON, which uses names | `"aggregationTemporality":"AGGREGATION_TEMPORALITY_DELTA"` is an unknown value; the receiver reads UNSPECIFIED |
| A receiver **MUST ignore** unknown fields | same section | Applies to us as a receiver: this SDK reads the collector's response, so a field OTLP adds later must not break an export |

The enum values themselves come from `metrics.proto`:
`AGGREGATION_TEMPORALITY_UNSPECIFIED = 0`, `DELTA = 1`, `CUMULATIVE = 2`.

Field **order** inside every message is the schema's **field-number** order.
Protobuf-JSON imposes none, so any order would be valid; field-number order is
chosen because it is *derivable from the document*, which is what makes the
hand-written expected bytes checkable line by line instead of taste. The visible
evidence that the order came from the schema and not from a preference:
`NumberDataPoint` puts `attributes` **after** the value, because it is field 7 —
it replaced a `labels` field that used to sit at 1.

### 3. Presence beats brevity: three fields are emitted at their zero.

Proto3-JSON says a serializer *should* omit a field holding its default. Three
fields here have **explicit presence**, where omission means something else
entirely, and one is emitted against the rule for a reason of its own.

- **`asInt` / `asDouble`** are `oneof` members. An omitted oneof means *no case
  selected*, not "the default" — so a counter sitting at 0 would encode as a
  `NumberDataPoint` with no value at all. Emitted always.
- **`sum` on a histogram point** is declared `optional double`. Present-and-zero
  means "the observations summed to zero"; absent means "no sum was recorded".
  This SDK always has one. Emitted always, and it is a pointer in the Go shape
  for exactly that reason.
- **`isMonotonic`** has no presence and *would* legitimately be omitted when
  false. It is emitted anyway. It is the single field that tells a Counter from
  an UpDownCounter (ADR 0044 §Decision 5), and a field that disappears precisely
  when it carries the surprising answer is a field a reader cannot trust. A
  receiver reads the same value either way; a human, and the test that pins
  these bytes, do not.

The mirror of the rule also holds: an empty `attributes` array, an absent scope
`version`, `description`, `unit`, `schemaUrl`, `flags`, `exemplars`,
`droppedAttributesCount`, histogram `min`/`max` are all **absent**, because this
SDK produces none of them and an always-empty field is a placeholder (rule 5).

### 4. A delta temporality is carried, and an unresolved one is REFUSED.

Carrying delta is the entire reason OTLP is worth having: it is the difference
between this wire and Prometheus, which has no temporality field at all and
therefore makes a delta snapshot unrepresentable (ADR 0044 §Decision 8). Here
`aggregationTemporality` is 1 or 2 and the fact travels.

What is *not* carried is `TemporalityUnspecified`, although the enum has a 0 for
it. The schema's own comment reads: **"UNSPECIFIED is the default
AggregationTemporality, it MUST not be used."** A receiver handed 0 either drops
the metric or guesses which window the number covers — and guessing is exactly
the failure ADR 0044 exists to prevent, since the same number under the two
settings is two different facts. So the encoder refuses, with
`OTLP_UNRESOLVED_TEMPORALITY` (`0.3.45.5`), and writes nothing.

Refusing is safe for the reason refusing a metric name is safe in the Prometheus
connector: a meter's temporality is **structure**, resolved once at
construction. A `SnapshotValue` can only carry an unresolved one if it was
hand-built or cast, so this fails on the first export or never — it cannot start
failing in production because of traffic.

There is no such thing here as "a delta temporality on a cumulative instrument".
`Temporality` is a field of the **metric**, not of the point, and one Meter
resolves it once for every metric it produces; the encoder reports what the
snapshot says and does not second-guess it. The only unrepresentable case is the
unresolved one above.

### 5. A bucket ladder OTLP cannot express is refused too.

`metrics.proto` states two invariants for an explicit-bucket histogram:
`bucket_counts` must be **one longer** than `explicit_bounds`, and the bounds
must be **strictly increasing**. A non-finite bound violates the second by
construction — the bucket above the last declared bound is *already*
`(bound, +infinity)`, so declaring `+Inf` creates a second, permanently empty
bucket over the same range, and `NaN` is not an ordering at all.

`SnapshotValue` can carry all three violations: `Meter.Histogram` takes the
bounds from the caller, sorts them, and does not dedupe or reject. So the
encoder checks, and refuses with `OTLP_INVALID_BUCKET_LAYOUT` (`0.3.45.6`).

This is where the two wires diverge on identical input, and the divergence is
worth stating: the **Prometheus connector skips** a non-finite bound, which it
can afford because `le="+Inf"` is a mandatory separate line there. Skipping here
would break the count relation, so the loss is named instead of silently
encoded. A bucket ladder is a literal at the call site — structure again, and
refused for the same reason.

### 6. Non-finite doubles are named, not fatal.

`encoding/json` refuses `NaN` and `±Inf` outright. Proto3-JSON does not: a
double is "a number or one of the special string values `NaN`, `Infinity`, and
`-Infinity`". A gauge is whatever was sampled, and a NaN reading is a legitimate
observation — so a custom `MarshalJSON` spells all three rather than failing an
entire export because one series has no number. Finite values render
shortest-round-trip (`strconv`, `'g'`, `-1`), every form of which is a valid
JSON number.

Two related renderings, decided rather than defaulted:

- **HTML escaping is OFF.** `encoding/json` turns `<`, `>` and `&` into `<`
  and friends by default, a defence for JSON embedded in a `<script>` element.
  An OTLP body never is, and OTel-conventional attributes carry URLs
  (`url.full`, `http.route`) whose query separator is exactly `&`. The default
  makes a readable payload unreadable and makes this SDK's bytes differ from
  every other producer's for identical input.
- **An unset `time.Time` encodes as 0.** `time.Time.UnixNano`'s result is
  documented as *undefined* when out of range, and the zero `Time` is that case:
  it returns `-6795364578871345152`, which cast to a `uint64` nanosecond
  timestamp reads as the year 2339. Zero is what the schema means by an unknown
  timestamp; a plausible wrong date is what no dashboard can detect. Pinned by
  a test that asserts the garbage value never appears.

### 7. The POST is a connector. It is bounded, it does not follow redirects, and it does not retry.

The emitter is a connector to an external system, and it is written like the
other one in this tree (`writer/nettransport`), not like a library call.

- **Timeout.** `DefaultOTLPTimeout` is 10 s — the OpenTelemetry protocol
  exporter specification's own default for `OTEL_EXPORTER_OTLP_TIMEOUT`, so the
  clamp lands on the specification's number rather than on one this SDK
  invented. Non-positive clamps; there is no "no timeout" setting, because a
  stalled collector must never wedge the goroutine that is scraping (ADR 0031).
  A caller-supplied `http.Client` is used **as-is**, deadline included — it is
  the seam for a proxy, an mTLS identity or an SSRF allowlist, and
  second-guessing it would defeat the seam.
- **A bounded response.** The response body is the one length a *remote* party
  controls in this exchange, so it is read through an `io.LimitReader`
  (`DefaultOTLPMaxResponseBytes`, 1 MiB). The **request** body is deliberately
  not capped: its size is a property of the caller's own cardinality, any
  SDK-chosen ceiling would be arbitrary (ADR 0031 §refuse), and the collector
  already answers `413` for one it will not take.
- **No redirects.** The default client returns `http.ErrUseLastResponse`
  (CWE-918). A `30x` from anything in front of the collector would otherwise
  bounce the POST — `Authorization` header included — at whatever host the
  response names, past an allowlist that only ever saw the configured endpoint.
  The unfollowed `30x` is then classified as a permanent rejection, which is
  what a misconfigured endpoint is.
- **The endpoint is a full URL used as-is**, and it is refused at construction
  when it is not absolute `http(s)` with a host and a non-root path
  (`OTLP_ENDPOINT_INVALID`, `0.3.45.7`). The path check is the one that earns
  its keep: a bare `http://collector:4318` **connects**, answers `404`, and
  looks exactly like a collector that is up. Refusing at wiring time is strictly
  earlier than refusing at the first scrape — and unlike the `resilience`
  constructors, nothing here publishes a signature that forces the refusal to be
  deferred into an always-failing value (ADR 0031's shape problem does not
  apply).
- **No retry, and no backoff.** The specification asks a client to honour
  `Retry-After` and otherwise back off exponentially. `internal/service/resilience`
  already ships that policy. A backoff hidden inside `Export` would be a second
  one a caller cannot see, tune or cancel — so what this exporter supplies is
  the **classification** a retry policy needs, in the exact shape
  `resilience.RetryConfig.Retryable` wants:

  ```go
  resilience.NewRetry(resilience.RetryConfig{
      MaxAttempts: 3,
      BaseDelay:   time.Second,
      Retryable:   metrics.OTLPRetryable,
  })
  ```

### 8. Three verdicts, from the specification's own three sections.

| Response | Sentinel | Retryable | Section |
|---|---|---|---|
| 2xx, `partialSuccess` unset or zero | none | — | Full Success |
| 2xx, `rejectedDataPoints != 0` | `OTLP_PARTIAL_SUCCESS` (`0.3.45.10`) | **no** | Partial Success — "The client MUST NOT retry" |
| 429 / 502 / 503 / 504 | `OTLP_EXPORT_UNAVAILABLE` (`0.3.45.9`) | yes | Retryable Response Codes |
| any other 4xx/5xx, and an unfollowed 3xx | `OTLP_EXPORT_REJECTED` (`0.3.45.8`) | no | Failures — "All other 4xx or 5xx … MUST NOT be retried" |
| transport fault, no response | `OTLP_EXPORT_UNAVAILABLE` | yes | All Other Responses — "the client SHOULD retry" |

The retryable set is spelled out rather than derived from the status class,
because **5xx is not retryable as a class**: a `500` or a `501` means the same
request will fail the same way. A test pins both halves.

Three decisions inside that table:

- **A partial success is an error.** It is an HTTP 200 and data was lost. A
  `nil` return would report a success that did not fully happen, on every
  scrape, and the whole point of the domain is that a number means what it says.
- **An unparseable 2xx body is a success.** The 200 already said the request was
  accepted; turning a collector's malformed or proxied response into a
  lost-data verdict would invent a failure, forever. It is also the posture the
  specification asks a receiver to take in the other direction — ignore what you
  do not recognise.
- **`rejectedDataPoints` is decoded from a number OR a string.** The
  specification says 64-bit integers are encoded as decimal strings "and either
  numbers or strings are accepted when decoding". Collectors and proxies differ,
  and a decoder that read only one form would silently read every partial
  success as a full one against the other kind.

### 9. Registered on stderr; the HTTP emitter is never registered.

`otlpjson` self-registers on **stderr**, like `text` and `prometheus`
(ADR 0030): importing a package must not arm a writer on a stream the process
may be using as a protocol channel.

`NewOTLPHTTPExporter` is **not registered at all**, and the reason is a step
beyond ADR 0030. The registry is reached by importing a package; arming a
*network client* from an import is strictly worse than arming a writer, because
there is no endpoint that could be a correct default and a wrong one turns every
`Export` into a POST at whatever answers on that address. It is constructed
explicitly, by a caller who names the collector, or not at all. A test asserts
no OTLP/HTTP exporter appears in `AvailableExporters()`.

## Consequences

- `internal/service/metrics` grows `exporter_otlpjson.go` (encoder + writer
  exporter) and `exporter_otlphttp.go` (the only file in the package that
  imports `net/http`). `pkg/v1/metrics` re-exports `EncodeOTLPJSON`,
  `NewOTLPJSONExporter`, `NewOTLPHTTPExporter`, `OTLPRetryable`,
  `OTLPHTTPConfig`, three constants and six sentinels.
- **Error codes**: `0.3.45.5`–`0.3.45.10`, all inside the block
  `internal/service/metrics` already owns. **No new `codeRangeOwners` entry**
  (ADR 0035).
- **The dependency budget is unchanged**: `bytes`, `encoding/json`, `io`,
  `math`, `net/http`, `net/url`, `os`, `strconv`, `strings`, `sync`, `time`.
  Nothing from `go.opentelemetry.io`, no protobuf runtime, no `x/sys`.
- **The conformance test does not test the encoder against itself.** The
  expected document is written by hand from `metrics.proto`, `common.proto`,
  `resource.proto` and the OTLP §JSON Protobuf Encoding section, with the field
  numbers named in comments beside each fragment. A test that decoded the
  encoder's own output would prove self-consistency — which is exactly the
  property a wrong field name or a mis-numbered enum preserves.
- **No benchmark, and therefore no `BENCH.md` entry** (rule 9). An export is a
  scrape-rate operation, not an observation-rate one; the allocation budget this
  package defends is on the *lookup* path, which this change does not touch. A
  benchmark here would pin a number nothing depends on.

## Deferred

- **OTLP/protobuf (binary).** It needs a protobuf encoder, and the only honest
  ways to get one are a code generator in the build or a hand-written wire
  writer per message. The JSON encoding is a first-class OTLP encoding — every
  collector accepts it — so binary buys throughput, not reach. It would register
  as `otlpproto`, beside `otlpjson`, and reuse the whole snapshot walk.
- **gzip request compression.** The specification makes it a MAY, with
  `Content-Encoding: gzip`. It is one more knob whose only correct value depends
  on the collector, and `transform` already exists for a caller who wants to
  compress a body themselves.
- **Environment-variable configuration** (`OTEL_EXPORTER_OTLP_*`). Reading the
  process environment from a constructor is a second, invisible configuration
  surface; this SDK has a `config` domain and an explicit struct.
- **A push loop.** Deciding *when* to collect and export is a scheduling
  concern, and `scheduler` (ADR 0041) already owns it. `Export` is one call.
- **Exemplars, exponential histograms, array attributes, `SchemaURL`,
  `description`/`unit`, `min`/`max` on a histogram point.** The model produces
  none of them (ADR 0044 §Deferred); the encoder gains the field on the day the
  model does.

## Why not

- **Import `go.opentelemetry.io/otel/exporters/otlp/…`.** Rejected for the
  reason ADR 0044 §Decision 1 rejected importing the model: it drags in `x/sys`
  (banned SDK-wide, ADR 0022 / ADR 0034) plus a protobuf runtime and a release
  cadence this repo does not control — in exchange for a JSON document that is
  under 500 lines of declarations. The interoperability lives on the wire, and
  the wire is reachable from `encoding/json`.
- **One surface: an `Exporter` that encodes and POSTs.** Rejected: it makes the
  bytes unreachable without a socket, so every schema question becomes a network
  question. The two-surface split costs one exported function.
- **Register the HTTP exporter with a default endpoint** (`localhost:4318`).
  Rejected: an import would then arm a network client at an address the caller
  never named. §Decision 9.
- **Retry inside `Export`.** Rejected: `resilience` exists, a hidden backoff
  cannot be tuned or cancelled by the caller who owns the scrape loop, and
  `Export` has no `context` to cancel it with. Classification composes;
  a private loop does not.
- **Map `TemporalityUnspecified` to the enum's 0.** Rejected: the schema says
  that value MUST NOT be used, and emitting it hands the receiver a guess about
  which window the number covers — the one failure ADR 0044 was written to
  prevent.
- **Skip a non-finite bucket bound, as the Prometheus connector does.**
  Rejected: it breaks the `len(bucket_counts) == len(explicit_bounds) + 1`
  invariant the schema states, so the payload would be malformed rather than
  merely lossy. §Decision 5.
- **Omit `asInt` when a counter is 0, and `isMonotonic` when false.** Rejected:
  the first leaves a data point with no value selected at all, and the second
  hides the flag exactly when it is the interesting one. §Decision 3.
- **Attach the collector's `errorMessage` to the partial-success error.**
  Rejected: it is unbounded remote-controlled text and an `errs` Field goes
  straight into structured logs. The rejected COUNT is what an operator alerts
  on; the text is in the collector's own logs.
- **Honour `Retry-After` when it is an HTTP-date.** Rejected: converting one
  means comparing the collector's clock to ours, which is the sort of quiet
  assumption that surfaces months later as a retry storm. The delta-seconds form
  is surfaced as a field; the date form reports no hint rather than a guessed
  one.

## References

- OTLP specification (OTLP/HTTP, §JSON Protobuf Encoding, §OTLP/HTTP Response,
  §Retryable Response Codes, §OTLP/HTTP Throttling, §All Other Responses) —
  <https://opentelemetry.io/docs/specs/otlp/>
- `opentelemetry/proto/metrics/v1/metrics.proto` — `AggregationTemporality`,
  `ResourceMetrics`, `ScopeMetrics`, `Metric`, `Sum`, `Gauge`, `Histogram`,
  `NumberDataPoint`, `HistogramDataPoint`
- `opentelemetry/proto/common/v1/common.proto` — `AnyValue`, `KeyValue`,
  `InstrumentationScope`
- `opentelemetry/proto/resource/v1/resource.proto` — `Resource`
- `opentelemetry/proto/collector/metrics/v1/metrics_service.proto` —
  `ExportMetricsServiceRequest`, `ExportMetricsServiceResponse`,
  `ExportMetricsPartialSuccess`
- ProtoJSON mapping (the generic rules OTLP inherits and, for enums, overrides) —
  <https://protobuf.dev/programming-guides/json/>
- OpenTelemetry protocol exporter configuration (`OTEL_EXPORTER_OTLP_TIMEOUT`
  default) — <https://opentelemetry.io/docs/specs/otel/protocol/exporter/>
- Impl: `internal/service/metrics/exporter_otlpjson.go`,
  `internal/service/metrics/exporter_otlphttp.go`, `pkg/v1/metrics/metrics.go`
- Rationale in place: `internal/service/metrics/CLAUDE.md` §The OTLP/JSON
  encoder + §The OTLP/HTTP emitter
- ADR 0044 (the model + §Decision 9 mapping table), ADR 0030 (registered
  destinations), ADR 0031 (clamp vs refuse), ADR 0026 (the retry policy),
  ADR 0035 (code ranges)
