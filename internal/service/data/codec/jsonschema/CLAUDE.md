<!-- updated: 2026-11-03T00:00:00Z -->
# internal/service/data/codec/jsonschema/

## Purpose

A JSON Schema 2020-12 validator for documents somebody else wrote — a webhook payload, an OpenAPI request body, another program's configuration — with the standard library alone (#882, ADR 0210). Not a codec Format. Public facade: `pkg/v1/data/codec/jsonschema`. Codes `0.3.103.*`, declared in `internal/core/data/codec/jsonschema`.

## Surface

| Symbol | Contract |
|---|---|
| `NewValidator(schema []byte, cfg Config) (*Validator, error)` | compiles once; `SchemaInvalid`, `KeywordRefused`, `RefUnresolved`, `FormatUnknown` |
| `Config` | `Resources map[string][]byte` (schemas a `$ref` may name, by URI; never fetched), `AssertFormats []string` (`date-time`, `uuid`, `email`) |
| `(*Validator).Validate(doc []byte, maxBytes int64) (validation.ReportValue, error)` | reads through `strictjson.Decode`; the report in validation's path grammar; an error only when the document was not read (strictjson's refusal) or `ReferenceLoop` |

## Contents

| File | Role |
|---|---|
| `jsonschema.go` | `Config`, `Validator`, `NewValidator`, `Validate` |
| `value.go` | the exact JSON tree: numbers as `big.Rat` from their literal, members in order; `equal` (RFC-style: numbers by value) |
| `compile.go` | the compiler: `index` (base URIs, `$id` resources, `$anchor`s — walking only schema keywords, so an `$id` in an `enum` is data), `resolveRef` (resource, then pointer or anchor), `compile` (memoized per schema object, so a `$ref` cycle compiles), the refused keywords, `checkDraft` |
| `keywords.go` | the validation vocabulary's builders, `builderFor` |
| `applicators.go` | the core and applicator builders |
| `formats.go` | the three asserted formats |
| `eval.go` | `schema`, `check`, `eval` — collecting or quiet |

## How it evaluates

- **Each keyword compiles to a `check`**, in document order; a boolean schema is its verdict.
- **Collecting and quiet evaluations.** `allOf`, `properties`, `items`, `$ref`, `dependentSchemas` and the branch `if` picks report their schemas' own violations; `anyOf`, `oneOf`, `not`, `contains`, `if`'s condition and `propertyNames` ask their schema quietly (yes or no, first failure stops) and report once themselves.
- **Codes.** validation's where one names the keyword — `required` / `dependentRequired` `CodeRequired`, the numeric bounds `CodeOutOfRange`, the six sizes `CodeLengthOutOfRange`, `enum` / `const` `CodeNotInSet`, `pattern` `CodePatternMismatch` — `CodeJSONSchemaViolation` otherwise. `Rule` is the keyword. Messages name the rule and the schema's bound, never the document.
- **A `$ref` cycle that consumes nothing** passes `maxEvalDepth` (512) and ends the validation with `ReferenceLoop`.
- **`pattern` is RE2**: a pattern Go's `regexp` does not read is refused at compile time.

## Tests

- `suite_external_test.go` — the official JSON-Schema-Test-Suite (`testdata/JSON-Schema-Test-Suite`, MIT, commit in its README.md): every required 2020-12 group, the remotes registered at the URIs the suite serves; 1 024 documents pass, 274 skipped in groups each named with its reason (`suiteSkips`: the refused vocabularies, and the groups needing the 2020-12 meta-schema, which uses `$dynamicRef`). A floor (`minSuitePassed`) catches a file gone missing.
- `jsonschema_external_test.go` — the report's grammar and codes, no violation quoting a planted secret, every refusal's code, strictjson's bounds, the formats; `FuzzNewValidator`.

## Benchmarks

`jsonschema_bench_test.go` — `NewValidator` on an order schema, `Validate10KiB` on a 10 KiB order; figures in `BENCH.md`.

## Do NOT

- Fetch a remote reference: a schema that names one has its caller register the document.
- Silently ignore a refused keyword: a schema relying on it would accept what its author refused.
- Decode into `any`: `float64` loses exactness, and `encoding/json` accepts duplicate names.
