<!-- updated: 2026-10-03T13:06:04Z -->
# internal/service/observe/metrics/

> Reference moved to `internal/service/observe/metrics/NOTES.md` (ADR 0253), under the same section titles: Contents, Series identity, Temporality: what Collect does, Observables, Cardinality policy, The text exporter, The Prometheus text exposition connector, The OTLP/JSON encoder, The OTLP/HTTP emitter, The exporters' conformance table.

## Purpose

In-memory `Meter` + lock-free instruments (`Counter`, `UpDownCounter`, `Gauge`,
`Histogram` and the three observable families) implementing
`core/observe/metrics`, plus three stdlib Exporters — a **text** diagnostic that
renders the whole OTel model, a **Prometheus** text-exposition **connector**
that deliberately does not, and **OTLP/JSON**, the native wire the model was
adopted for, which loses nothing. All three are registered to **stderr** on
import (ADR 0030); the OTLP/**HTTP** emitter is constructed explicitly and never
registered. Stdlib-only, cross-OS. ADR 0027 / ADR 0044 / ADR 0048 / ADR 0067.
Emits core sentinels `0.2.9.*` and the block `0.3.45.*` it was allocated for
what a wire format refuses — both declared in `internal/core/observe/metrics`
since ADR 0160 §2; this package declares none and names them `coremetrics.X`.

Instruments are keyed by name **and typed attribute set** — one name plus one
attribute set is one **series** — with a per-name cardinality bound that folds
the excess into a single aggregated overflow series.

## Configuration

`MeterConfig.resolve` clamps or refuses every knob; none is left inert
(ADR 0031).

| Knob | Unset | Rule |
|---|---|---|
| `MaxSeriesPerInstrument` | `DefaultMaxSeriesPerInstrument` (2000) | non-positive **clamps**; there is no "unbounded" |
| `Temporality` | `Cumulative` | unset **clamps** — it describes what the meter does; a cast value **refuses** (`InvalidTemporality`) |
| `Resource` | `service.name=unknown_service` | **clamps** to the value the OTel spec mandates for exactly this case |
| `Scope` | `Name = DefaultScopeName` | **clamps** to the one truthful default; `Version` stays empty because the spec makes it optional |
| `Clock` | `clock.System` | the same default every other port in this SDK takes |

## What the Prometheus connector loses

This exporter is a **deliberately lossy connector**, not a rendering of the
SDK's data model. The model is OpenTelemetry's; the exposition format predates
most of it and has no field for the rest; the description (`# HELP`) is the one
part of the OTel Metric this connector carries. It is kept because a Prometheus
deployment is a real destination — on the condition that its losses are named.
Each row below has an executable test.

| Lost | What happens | Why it is a loss and not a bug |
|---|---|---|
| **Temporality** | a delta snapshot is **REFUSED**, `UNSUPPORTED_TEMPORALITY` (`0.3.45.4`), nothing written — and so is an unresolved or cast temporality, which the OTLP encoder refuses too, rather than being emitted as if it were cumulative | the format has no temporality field and a Prometheus server reads every counter as cumulative — `rate()` differences successive scrapes itself. Handing it deltas means differencing numbers that are already differences (the reported rate becomes the second derivative) and every window smaller than the last reads as a counter reset. Nothing in the document would say so and no dashboard would look broken. Refusing is safe for the same reason refusing a name is: a meter's temporality is fixed at construction, so this fails on the first scrape or never |
| **The attribute's TYPE** | every value is rendered to a string; `Int64("v", 1)` and `String("v", "1")` — two series in the snapshot — become ONE series on the wire | a Prometheus label value **is** a string; there is no second option and no encoding avoids it. Unlike a mangled NAME, there is no injective alternative here, which is exactly why this one is documented rather than refused. The rendering follows the OTel→Prometheus interoperability specification |
| **`Resource`** | dropped entirely; no `target_info` metric is emitted | `service.name` cannot even be SPELLED — a Prometheus label name is `[a-zA-Z_][a-zA-Z0-9_]*` and the dot is outside it. The interoperability spec answers this by mangling the key; this connector refuses non-injective name rewriting everywhere else in this very file, and consistency with our own rule beats consistency with theirs. Whatever producer identity a Prometheus deployment has comes from the scrape target's own labels (`job`, `instance`), which the server attaches |
| **`InstrumentationScope`** | dropped entirely | same wall: the format has no place for a second identity, and `otel_scope_name` would be an invented label the caller never wrote |
| **OTel-conventional attribute KEYS** | `http.request.method` is **REFUSED** by name (`INVALID_LABEL_NAME`) | the sharpest consequence of adopting the model: the dotted spelling is what the semantic conventions specify, and this wire cannot carry it. The refusal is loud and deterministic — a key is a literal at the call site — which is the whole reason refusing beats mangling |
| **Exemplars** | dropped: the exposition is byte-identical with and without them (`TestOTLPCarriesASumExemplarAsWritten`) | the text format has no field for one — exemplars are OpenMetrics' and the protobuf exposition's; OTLP/JSON carries every one (#869) |
| **An exponential histogram's buckets** | the family keeps `le="+Inf"`, `_sum` and `_count` (`TestExponentialHistogramOnEveryExporter`) | they are a native histogram's, which only the protobuf exposition carries; spelled as classic `le` buckets they would mint a series per bucket and move the `le` set at every scale change. The Prometheus client exposes a native-only histogram in text the same way |
| **The `_sum` of a source that keeps none** | a `NoSum` histogram — the runtime's — gets no `_sum` line | there is no reading to print; a forged 0 would make `rate(_sum)/rate(_count)` a mean of zero |

## The transport is shared, the vocabulary is not

Everything OTLP that is not this signal's payload lives in
`internal/service/observe/internal/otlp`, shared with `observe/trace`: the
proto3-JSON scalars, the `KeyValue`/`AnyValue`/`ResourceMessage`/`ScopeMessage`
messages, the single-document marshal, the NDJSON stream, the lenient
`rejected` decode and the whole OTLP/HTTP sender. The two signals differed only
in their codes, their wording and the name a partial success is counted under,
which reach the shared code as `otlpSignal` (an `otlp.SignalSpec`) and the two
`EXPORT_FAILED` wraps — so every error still reads `0.3.45.*` and this
package's words, byte for byte, and the tests asserting both ran unchanged
across the move. The rules this section and the next describe are enforced
THERE, and restated here because this is where a metrics reader looks.

## Conventions

- **Lock-free instruments.** The meter takes only the READ lock to resolve an
  existing series; the write lock is held for creation alone. That is what makes
  a per-observation lookup viable at all — attributes move the lookup from
  start-up onto the request path.
- **Zero allocations on the lookup path**, attributed or not, and on the
  overflow path. The sorted attribute set and the encoded key live in stack
  arrays, and the map is indexed with `string(scratch)`, which does not copy. A
  key is converted to a `string` in exactly one place — `retain`, on insertion,
  where the map keeps it. Gated by `meter_alloc_test.go`; see BENCH.md.
- **`NewMeter` and `NewMeterWithConfig` are one-line wrappers over
  `newMemMeter`, and that is load-bearing.** Both must stay INLINABLE: inlining
  is what carries the concrete meter type to a caller's call sites, and that is
  what lets the compiler prove a variadic attribute slice does not escape.
  Behind an opaque interface it must assume it does, and every attributed
  observation costs one small heap allocation. Measured — growing either
  constructor past the inlining budget fails the alloc gate.
- **`admit` takes the series identity in FOUR separate arguments**, and the
  meter back-pointer lives on the store instead. Go's escape analysis is
  field-insensitive on a struct parameter, so grouping (name, kind, key, attrs)
  into one value makes the whole group escape the moment the name is stored on
  an entry — two allocations per observation. Measured.
- **The stores are keyed flat** by the whole series key, not nested by name: one
  map read resolves an attributed fetch where a nesting would cost two.
- **A description belongs to the NAME too, but NOT to `nameState`** (which
  carries a KIND a description does not imply — storing it there would force
  minting the instrument before documenting it). It lives in its own
  `descriptions map[string]string`, nil until the first `Describe`; the nil map
  reads as empty, so an undescribed meter allocates nothing. Nothing on the fetch
  path reads it; gated by `TestDescribedMeterLookupIsAllocationFree` and priced
  in BENCH.md §ADR 0067 (~25 ns per described NAME per scrape, zero per
  observation).
- **Kind and bound belong to the NAME**, not the series (`nameState`) —
  attributes vary within one metric, its kind and its quota do not. Four
  instrument kinds map to one output GROUP, because a Counter and an
  UpDownCounter produce one point shape.
- **`Collect` carves one arena per group** into exactly-sized per-name windows
  (the meter's own series tally), yielded through a range-over-func; growing
  each name's slice instead costs an allocation per name plus doubling copies,
  scaling with cardinality on every scrape.
- **`Collect` is serialised against itself** by `collectMu`: it mutates under
  delta temporality, and under any temporality once an observable is registered.
- **Registration via `var Text = metrics.RegisterExporter(...)`** (and
  `var Prometheus = …`, `var OTLPJSON = …`) — no `init()`.
- **An OTLP surface never touches the observation path.** `EncodeOTLPJSON`
  hands a message tree to `encoding/json` at SCRAPE rate; the allocation budget
  is per OBSERVATION. Do not hand-append bytes: `encoding/json`'s escaping is a
  correctness property, and no measurement asks for the trade.
- **All three registered defaults write to `os.Stderr`** (ADR 0030): an import
  must not arm a writer on a stream the process may use as a protocol channel.
  stdout is reachable only explicitly (`NewTextExporter(name, os.Stdout)`), a
  scrape endpoint hands the Prometheus exporter its `http.ResponseWriter`, and
  the OTLP/HTTP emitter is **not registered at all**. One regression test per
  surface.
- **The two TEXT exporters escape STRING attribute values** (`\\`, `\"`, `\n`)
  through the shared `appendEscapedValue`; the OTLP encoder does not need it,
  because `encoding/json` owns JSON string escaping and doing it twice would
  double every backslash on the wire. A value is data; unescaped, one containing a
  quote or a newline forges a line a reader parses as another series.
- Cross-OS: 100 % portable (sync/atomic/math/strconv/time/encoding-json/net-http).

## Do NOT

- Import `runtime/metrics` here, or anything that does: `framework/app` imports this
  package, and every program that imports `framework/app` would pay that package's
  initialisation. The runtime's instruments live in `goruntime/` (#868).
- Discard writer errors — each exporter buffers into `[]byte` then does one
  `Write` with a wrapped `EXPORT_FAILED` on failure.
- Add an `init()`, or point a *registered* exporter at `os.Stdout`: the import
  is invisible at the call site, so the default must be the stream nobody parses.
- In the Prometheus connector: transliterate a metric or attribute key (`_`
  merges distinct instruments), emit a `target_info` metric to smuggle the
  Resource through (the same mangling, on `service.name`), let a delta or an
  unresolved snapshot reach the wire, invent a fourth escape sequence (the 0.0.4
  parser rejects anything but `\\`, `\"` and `\n`, so a `\r` costs the whole
  scrape), escape a double quote in a `# HELP` docstring (not a quoted token), or
  emit a `# HELP` for an empty description.
- Put a description on a data POINT or in the series key (it is
  non-identifying), or thread it through `Counter`/`Gauge`/`Histogram` (a second
  string on the variadic call the compiler must prove non-escaping, and two call
  sites could disagree about one metric's documentation).
- Convert a series key to a `string` before a map read. `m[string(b)]` does not
  allocate; `k := string(b); m[k]` does, once per observation.
- Inside `Collect`, clone a series' attribute set (it is shared with the
  snapshot on purpose — `internal/core/observe/metrics/CLAUDE.md` §The snapshot
  shape) or swap an OBSERVED series to zero under delta (its callback already
  stored the window).
- Grow `NewMeter` / `NewMeterWithConfig` past the inlining budget (§Conventions),
  or add an "unbounded" cardinality setting.
- Break an OTLP/JSON rule (§The OTLP/JSON encoder): emit
  `AGGREGATION_TEMPORALITY_UNSPECIFIED` (0), an enum by NAME or a 64-bit integer
  as a JSON number (it loses the low bits past 2⁵³); omit `asInt`/`asDouble`,
  the histogram `sum` or `isMonotonic` at their zero; skip a non-finite bound the
  way the Prometheus connector does (it breaks
  `len(bucketCounts) == len(explicitBounds) + 1`); turn HTML escaping back on, or
  reach for `json.Marshal` instead of the configured `json.Encoder`; add a field
  to the tree without writing it in its `appendJSON`, or let the appenders drift
  from `otlp.Marshal` — `TestOTLPAppendJSONIsMarshalsRendering` is the gate.
- Loosen the OTLP/HTTP emitter (§The OTLP/HTTP emitter): register it or give it
  a default endpoint; build its default client without a `Transport` of its own
  (a nil one is `http.DefaultTransport`, which any code in the process can
  empty), or replace, wrap or clone a caller-SUPPLIED client's `Transport`;
  drain a response without a bound, or bound the drain by `MaxResponseBytes`
  (that knob caps what is read into memory, and a caller who lowered it must
  not lose connection reuse on a conforming response); retry inside `Export` (`resilience` owns backoff, `OTLPRetryable`
  classifies, and the context `ExportContext` takes bounds one round trip, which a hidden loop would spend); attach
  the collector's `errorMessage`, a `Retry-After` HTTP-date or the endpoint to an
  error — all three are remote-controlled or secret, and a Field goes into
  structured logs.

## Verification

```
bazel test --config=race //internal/service/observe/metrics:metrics_test
bazel test --config=alloc //internal/service/observe/metrics:metrics_test   # the !race alloc gates
# Fallback (no Bazel):
cd internal/service && GOWORK=off go test -race ./observe/metrics/...
```
