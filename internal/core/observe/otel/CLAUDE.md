<!-- updated: 2026-10-03T00:00:00Z -->
# internal/core/observe/otel/

## Purpose

The OpenTelemetry types **every signal shares**, and nothing else: the typed
attribute (`common/v1` `KeyValue`/`AnyValue` → `AttrValue`), the producer
identity (`resource/v1.Resource` → `ResourceValue`) and the instrumentation
identity (`common/v1.InstrumentationScope` → `ScopeValue`). `internal/core/observe/metrics`
and `internal/core/observe/trace` both sit ABOVE this package; neither borrows the
other's model.

This is the extraction ADR 0051 §Decision 2 named and deferred: the three types
lived in `core/observe/metrics` only because metrics was the first signal this SDK
implemented, which made `core/observe/trace` import `core/observe/metrics` — and, through
`pkg/v1/observe/logger`'s trace correlation (ADR 0062), put the whole metrics port in
front of every program that only wanted a log line. That edge is gone:
`pkg/v1/observe/logger` now reaches `core/observe/trace` and this package, and no metrics code.

Like both signals above it, it implements the specification and imports nothing
from `go.opentelemetry.io` — nor anything of this SDK: it is standard library
only, which is what lets every signal (and the logger's trace correlation) sit on
it for the price of three small files.

Code range: **none**, deliberately — see §The refusal is the caller's.

## Contents

| File | Surface |
|---|---|
| `attr_value.go` | package doc + `AttrKind` (+ the five kinds) + `AttrValue` + the four constructors (`String`/`Bool`/`Int64`/`Float64`) + accessors + `AppendIdentity`/`AppendText` + `CompareAttrKey`/`CompareAttrValue` + `ValidateAttrs`/`SortAttrs`, both taking the caller's refusal |
| `resource_value.go` | `ResourceValue` + `NormalizeResource` (taking the caller's refusal) + `ServiceNameKey`/`UnknownService` |
| `scope_value.go` | `ScopeValue` + `NormalizeScope` (taking the signal's default name) |
| `model_external_test.go` | the canonical text, the injective identity, the refusal seam, Resource and Scope normalisation |
| `attr_bench_test.go` | the prices of the model — `BENCH.md` |

`pkg/v1/observe/metrics` and `pkg/v1/observe/trace` alias these types under shorter names —
`Attr`, `AttrKind`, `Resource`, `Scope` — and both alias THESE, so
`metrics.Attr` and `trace.Attr` are one type (ADR 0074: an alias points at the
layer that owns the type, and the port types of two signals are owned here).

## The refusal is the caller's

An unusable attribute set — an empty key, the same key twice (including twice
under two kinds), a value no constructor set — is a programmer error at the call
site that wrote it, and every signal panics on it. The question is WHOSE code
the panic carries, and the answer is: the signal whose API was misused.

So this package declares no code and owns no sentinel. `ValidateAttrs`,
`SortAttrs` and `NormalizeResource` take the caller's sentinel — typed `error`,
an `*errs.Error` in every caller — and panic with exactly its message, reading
it for nothing else:

| Caller | Wrappers | Code |
|---|---|---|
| `internal/core/observe/metrics` | `ValidateAttrs`, `NormalizeResource` | `INVALID_ATTRIBUTE` `0.2.9.4` — unchanged from before the move |
| `internal/core/observe/trace` | `ValidateAttrs`, `SortAttrs`, `NormalizeResource` | `INVALID_ATTRIBUTE` `0.2.20.7` — new; a span's refusal used to borrow the metrics code because the model lived there |

Owning a code here was considered and refused. The range table
(`design/sdk.yaml`'s `codes.ranges`, which kit writes into `internal/kernel/errs/codes_gen_test.go` (ADR 0164)) grants a whole
`MM.LL.PP` slot to one package, so `0.2.9.4` cannot move here without moving the
rest of the metrics slot with it, and a NEW shared code would have changed the
value `pkg/v1/observe/metrics.InvalidAttribute` documents — codes never change.

`TestEveryRefusalCarriesTheCallersCode` pins the seam with two stand-in
sentinels: the same defect, handed two refusals, panics twice differently.

## Why the two normalisers are functions, not methods

`ResourceValue.Normalized()` and `ScopeValue.Normalized()` existed on the
types while they lived in `core/observe/metrics`, and both are gone:

- a method on the shared `ResourceValue` would have to pick ONE signal's
  refusal for both;
- `ScopeValue.Normalized()` filled the METRICS package's name, so
  `trace.Scope{}.Normalized()` — reachable through the `pkg/v1/observe/trace` alias —
  stamped a span batch with `…/pkg/v1/observe/metrics`, the exact wrong answer ADR 0051
  §Decision 2 guards against. `NormalizeScope` takes the default as a parameter,
  and each signal passes its own `DefaultScopeName`.

Removing a method from a type `pkg/v1` aliases is a published-shape change,
permitted while the module is v0 and stated as one (ADR 0040).

## The model, and which parts are here

| Concept | Here | Note |
|---|---|---|
| Typed attributes (`string`/`bool`/`int64`/`double`) | yes | `AttrValue` + four constructors |
| Homogeneous ARRAY attributes | **deferred** | ADR 0044 §Decision 2 — a boxed field on the hot path's stack scratch |
| Resource | yes | `ResourceValue`, carried once per payload |
| `service.name` + `unknown_service` | yes | `ServiceNameKey` / `UnknownService` — the key a backend correlates a trace with a metric on, so both signals read it from one place |
| InstrumentationScope | yes (name + version) | `ScopeValue`; `SchemaURL` + scope attributes deferred (ADR 0044 §Deferred) |
| A default scope name | **no** | each signal owns its own `DefaultScopeName`; sharing one is the wrong answer ADR 0051 names |
| The overflow attribute key | **no** | `core/observe/metrics.OverflowAttrKey` — only a meter writes it |

## Conventions

- **An attribute KEY and an attribute KIND are structure; a VALUE is data.** Key
  and kind are written at the call site and constant for the process; values
  vary per observation. That asymmetry is why an unusable key or an unset value
  is a programmer error a signal panics on, while an unbounded stream of values
  is a runtime condition the meter absorbs.
- **An attribute set is a SET**: order is not identity, and a duplicate key is a
  refusal, not a last-one-wins merge — including when only the KIND differs.
- **The kind tag is part of the identity.** `AppendIdentity` writes it before the
  value, so `String("v", "1")` and `Int64("v", 1)` never encode alike. A double
  is keyed on its IEEE-754 BIT PATTERN, so ±0 and two NaN payloads are distinct;
  `CompareAttrValue` breaks the same ties, or a sort would call two distinct
  series equal and a snapshot would stop being deterministic.
- **`SortAttrs` always clones**: it sorts in place, so it must own the array
  before it orders it. The meter's hot path uses a stack buffer instead and
  calls `ValidateAttrs` on it; the cold path costs one allocation and `BENCH.md`
  prices it.

## Do NOT

- **Import `go.opentelemetry.io/*`.** The model is a specification; the code is
  ours (ADR 0044 §Decision 1, ADR 0051).
- **Declare an error code here**, or pick a signal's sentinel for the caller.
  See §The refusal is the caller's.
- **Import `internal/core/observe/metrics` or `internal/core/observe/trace`.** They are above
  this package; the edge runs one way, and it is the reason the package exists.
- **Add a default scope name, or a method that needs one.**
- **Twin a type here in a signal package.** `trace.Attr` and `metrics.Attr` are
  one type; a second declaration would encode identically, pass every review and
  surface only at the exemplar boundary ADR 0051 §Decision 2 describes.
  `internal/core/observe/trace`'s `TestAttributesAreTheSameTypeAsMetrics` stops
  compiling the day one appears.

## Verification

```
cd internal/core && GOWORK=off go test -race ./observe/otel/
bazel test --config=race //internal/core/observe/otel:otel_test
```
