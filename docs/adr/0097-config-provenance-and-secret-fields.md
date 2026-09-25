# ADR 0097 — a load says where each value came from, and a secret field is loaded as written

- **Status**: Accepted
- **Date**: 2026-09-25
- **Deciders**: SDK maintainers
- **Related**: [ADR 0028](0028-sdk-config-domain.md) (the `config` domain), [ADR 0061](0061-sdk-config-schema.md) (the schema this reuses the vocabulary of), [ADR 0096](0096-a-secret-is-a-value-no-rendering-writes-down.md) (the `secret.Value` a field holds), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a sibling, not a widened `Source`), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (new shapes, published)
- **Closes**: ADR 0028 §Deferred — "Secret redaction on decoded values + a `Secret` field type"; ADR 0061 §Deferred — "Marking a key secret so a `--show-config` redacts it"

## Context

A service that loads its configuration from a file, the environment and a
schema's defaults cannot answer the first question an operator asks when a
setting is wrong: *where did this value come from?* `Load` merged the layers and
kept no record of which one won, so a framework that wanted to show its settings
— every `KIT_*` variable, its origin, "code", "env" or "default" — had to
re-implement the merge beside the SDK's, and two merges drift.

The second gap is the one ADR 0028 deferred by name. ADR 0096 now gives a secret
a type, `secret.Value`, which decodes from a JSON string like a `string` field
does and refuses a JSON number. The environment source, however, coerces every
value that is one whole JSON document: `8080` becomes an `int64`, which is right
for a port — and `1e3` becomes `1000`, `0.10` becomes `0.1`, and a twenty-digit
token becomes a `float64` that has lost its tail. A numeric secret in the
environment would therefore be refused (the honest failure) or, for a type that
accepted numbers, silently re-spelled (the dangerous one).

## Decision

### 1. A traced variant of each load, the existing ones unchanged

`LoadWithOrigins[T]` and `LoadSchemaWithOrigins[T]` run exactly the pipeline
`Load` and `LoadSchema` run — the same merge order, the same key pass, the same
decode and the same verdicts — and additionally return one `Origin` per LEAF key
of `T`, sorted by key:

```go
type Origin struct { // core/config.OriginValue
    Key    string // "database.dsn" — the dotted grammar of ADR 0061
    Layer  string // "default", "file", "env", a source's own kind, "source", or ""
    Detail string // "APP_DATA_DIR", "/etc/app.json", or ""
    Secret bool   // the field holds a secret.Value
}
```

`Load`, `LoadSchema`, `Source`, `Validator` and `Watcher` keep their signatures:
a caller who does not ask for provenance does not pay for the report, and a
schemaless load stays spellable. A key is attributed to the LAST layer that
supplied it — the one whose value the merge kept — and an explicit `null`
counts as supplied, as it does for `Required` (ADR 0061). A key absent from the
merged fold has an empty `Layer`: no layer supplied it, or a later layer erased
it by replacing one of its tables with a scalar or a `null` — either way its
field holds its Go zero, and the layer that first supplied it did not decide
that. Tables are not reported; their members are. On a failed load no origin is
returned.

### 2. A source describes itself through a sibling, not a widened port

`Source` is frozen at one method (ADR 0039), so provenance arrives as an
optional sibling discovered by type assertion:

```go
type Describer interface { // core/config.Describer
    Describe(key string) (layer, detail string)
}
```

`EnvSource` answers `("env", <the variable>)` — for a nested key, the variable
that supplied its top-level table — by re-running exactly the match its `Load`
ran, the last matching variable winning as it does there. `FileSource` answers
`("file", <its path>)`. The schema's default layer, and `Schema.Source()`,
answer `("default", "")`. A source that implements nothing is reported as
`("source", "<its position>")`, counting from 0 in the sources the load was
given. `Layer` is a string rather than an enum on purpose: a framework's own
code-defaults source names itself `"code"`, and a closed set would force it to
lie.

A `Describer` MUST NOT return a value in `detail`. The loader never puts one in
an origin, and the contract says a source may not either.

### 3. A secret field is marked, and loaded as written

A key whose field holds a `secret.Value` — directly or as the element of a
pointer, slice, array or map — is reported `Secret: true`. The set is resolved by
the same type walk ADR 0061's vocabulary uses, so "a secret key" and "a key" can
never disagree; a schema resolves it once at construction, and a schemaless load
walks each target type once and memoises the answer.

For a DIRECT secret key — a `secret.Value` field, or a pointer to one — that
the ENVIRONMENT supplied last, the loader replaces the coerced value with the
variable's raw text before the decode, on every entry point, traced or not. A
field holding secrets inside a slice or a map is marked `Secret` but keeps the
coerced value: its variable holds a JSON document, `["a","b"]`, whose decoded
form is what the field's decode needs, and whose elements were written as
strings. `12345`, `1e3`, `true`, `null` and a twenty-three-digit
token all arrive exactly as written. Only a top-level key can be one variable; a
secret nested inside a table the environment supplied as JSON was written as
JSON, quotes included, and decodes as written. Everything else about the
coercion is unchanged — `APP_PORT=8080` still becomes an `int64`.

### 4. No value in any error, report or origin

Origins carry no values by construction. The one error a secret field can
produce on the way in — `secret.ValueRefused`, for a number written in a file —
names nothing it was given, and `CONFIG_DECODE_FAILED` carries it unchanged. A
test asserts that the refused digits appear in neither the message, the private
text, nor any field.

## Consequences / Semantics

- A framework shows every setting with its origin by calling
  `LoadSchemaWithOrigins` and rendering the report, masking the `Secret` ones —
  without a second merge.
- A numeric secret in the environment loads, exactly; a numeric secret in a file
  is refused loudly, with the fix (quote it) in the verdict's text.
- Measured on darwin/arm64 (the config `BENCH.md` records the table): a
  schemaless `Load` goes from 1.69 µs / 16 allocations to 1.71 µs / 17 — the
  layers slice — once the per-type memo is in; without the memo the walk
  doubled it, which is why the memo exists. `LoadSchema` gains one allocation.
  `LoadSchemaWithOrigins` costs 4.79 µs / 51 allocations against 3.63 µs / 33.
- The loader gains one piece of package state: a `sync.Map` from target type to
  its secret keys, one entry per configuration type a program declares.

## Breaking changes

None. Four names are added to `pkg/v1/config` (`Origin`, `Describer`,
`LoadWithOrigins`, `LoadSchemaWithOrigins`) and four constants (`LayerDefault`,
`LayerFile`, `LayerEnv`, `LayerSource`). `EnvSource`, `FileSource` and
`Schema.Source()` return the same `Source` they did, which now also implements
`Describer`. No existing load changes its result for a type without a
`secret.Value` field; a type WITH one could not have existed before ADR 0096.

## Alternatives considered

- **Widen `Source` with a `Describe` method.** Refused by ADR 0039: every
  third-party source would stop compiling.
- **Return origins from `Load` itself.** Rejected: it changes a published
  signature, and makes every caller discard a report most never read.
- **A `LoadDescribed() (values, details)` sibling**, loading and describing in
  one call. Rejected: it forces a source to hold per-load state to answer, or to
  return a second map of the same size; `Describe(key)` lets the environment
  source re-derive the variable exactly and statelessly.
- **Undo the environment coercion for every string field**, not only secrets.
  Rejected: it would change the ADR 0028 contract for existing callers (a field
  typed `any`, or `json.Number`, reads the coerced form today). The secret field
  type is new, so bypassing the coercion for it changes nothing that existed.
- **Let `secret.Value` accept JSON numbers** instead of bypassing the coercion.
  Rejected in ADR 0096: the number reaching the decoder has already been
  re-spelled, so accepting it stores a secret nobody wrote.
- **Report `Secret` from a struct tag** (`secret:"true"`). Rejected: the type is
  the declaration. A tag can be forgotten on a field that still holds a
  password; a `secret.Value` field cannot be printed by accident either way.

## Deferred

- **Origins for keys below a map or a slice** — `labels.env` when `labels` is a
  `map[string]string`. The vocabulary stops at such a key (ADR 0061) and so does
  the report; the whole key is attributed to the last layer that touched it.
- **A `--show-config` renderer.** Presentation belongs to the framework; the
  report is the mechanism.
- **Provenance across a `Watcher` reload** — a diff of two reports is the
  caller's to compute, as ADR 0061 deferred `Diff` for values.

## Verification

- `internal/service/config/origins_external_test.go`: a schema default, a file
  and the environment attributed key by key, with the variable and the path, the
  report sorted; an undescribed source named by position; numeric, boolean, null
  and JSON secrets arriving as written through `Load` AND `LoadWithOrigins`; a
  numeric secret in a file refused with the digits in no message, private text or
  field; the three in-tree describers; a nil schema refused.
- `pkg/v1/config`: the same through public names.
- The existing config suites, unchanged, stay green.

## References

- `internal/core/config/origin.go`, `internal/service/config/origins.go`,
  `internal/service/config/BENCH.md` §ADR 0097
- ADR 0028 §Decode strategy (the coercion this bypasses for secrets)
