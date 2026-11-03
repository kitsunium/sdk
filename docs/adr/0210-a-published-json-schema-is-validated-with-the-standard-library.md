# ADR 0210 — a published JSON Schema is validated with the standard library

- **Status**: Accepted
- **Date**: 2026-11-03
- **Deciders**: SDK maintainers
- **Related**: [ADR 0102](0102-a-document-somebody-else-wrote-is-decoded-one-way-or-not-at-all.md) (strictjson, the reading this validator goes through), [ADR 0046](0046-sdk-validation-domain.md) (the report and its path grammar), [ADR 0156](0156-the-public-module-links-the-standard-library-and-nothing-else.md) (standard library only), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) (the code range)

## Context

`strictjson` reads a document somebody else wrote one way or refuses it, and
`validation` constrains Go values with located violations. A document whose
contract is a published JSON Schema — a webhook payload, an OpenAPI request
body, another program's configuration — had no validator: a product took the
dependency ADR 0156 forbids, or checked by hand ([#882](https://github.com/kitsunium/sdk/issues/882)).
ADR 0046 refused a tag-borne language of the SDK's own; a JSON Schema is a
standard document the caller supplies, not a vocabulary the SDK invents.

## Decision

### D1 — `codec/jsonschema`, a validator, not a Format

`internal/service/data/codec/jsonschema` (facade `pkg/v1/data/codec/jsonschema`,
codes `0.3.103.*`) compiles a 2020-12 schema once — `NewValidator(schema,
Config)` → `*Validator`, immutable and safe for concurrent use — and validates
documents with it: `Validate(doc, maxBytes)`. It registers no Format, as
`strictjson` and `jcs` register none. The constructor is `NewValidator`, not
the issue's `Compile`, because the type it builds is the `Validator`.

### D2 — the core, applicator and validation vocabularies; the rest refused by name

Compiled: `$ref` (within the document, and to the resources `Config.Resources`
registers by URI — a remote reference is refused, never fetched), `$id`,
`$anchor`, `$defs`, `allOf` / `anyOf` / `oneOf` / `not`, `if` / `then` /
`else`, `dependentSchemas`, `prefixItems` / `items` / `contains` with
`minContains` / `maxContains`, `properties` / `patternProperties` /
`additionalProperties` / `propertyNames`, and every validation keyword.
Numbers are compared exactly (`math/big.Rat` from the literal): `1` and `1.0`
are one number and `multipleOf: 0.01` has no float error. `format` is an
annotation unless `Config.AssertFormats` names it — `date-time` (RFC 3339),
`uuid`, `email` (an addr-spec as `net/mail` reads one); naming another is
refused. Refused by name, `KEYWORD_REFUSED`: another draft's `$schema`,
`$dynamicRef` / `$dynamicAnchor` (and the 2019-09 `$recursive*`),
`$vocabulary`, `unevaluatedItems` / `unevaluatedProperties`, the content
vocabulary. A `pattern` is read by Go's `regexp`: a lookaround or a
backreference ECMA-262 accepts and RE2 does not is refused when the schema is
compiled, never silently mis-matched.

### D3 — the document is read through strictjson; the report is validation's

`Validate` reads `doc` with `strictjson.Decode` — the byte bound, one value,
no duplicated member name, errors that never quote the input — and returns a
`validation.ReportValue`: each violation located in the validation package's
grammar (`items[2].sku`), its `Rule` the keyword, its `Code` validation's own
where one names the keyword (`required`, the numeric bounds, the sizes,
`enum` / `const`, `pattern`) and `CodeViolation` (`0.3.103.5`) otherwise.
`anyOf`, `oneOf`, `not` and `contains` ask their schemas quietly and report
once; `allOf`, the applicators and the branch `if` picks report their
schemas' own violations. No message quotes the document — the ADR 0046 test
shape plants a secret in every failing position. An error means the document
was not read: strictjson's refusal, or `REFERENCE_LOOP` for a `$ref` cycle
that consumes nothing.

### D4 — the official suite is the acceptance

`JSON-Schema-Test-Suite` (MIT) is vendored as testdata — `tests/draft2020-12`'s
required files and `remotes/draft2020-12`, registered at the URIs the suite
serves them on. At commit `ed0ebd31` every document of every group passes
(1 024) but the groups that use a refused keyword or need the 2020-12
meta-schema (which uses `$dynamicRef`), 274 documents, each group named with
its reason in `suite_external_test.go`.

## Consequences

- A product validates a published contract with the SDK alone, and its
  violations look like the validation package's.
- Validation costs about 90 µs per KiB of document on the reference machine
  (`BENCH.md`): every value is read into an exact tree and every path is built
  as it is visited. Fast enough for a request body; not a streaming validator.
- The unevaluated vocabulary is the most likely to be asked for next; it needs
  the annotation results this evaluator does not keep.

## Breaking changes

None: a new package.

## Alternatives considered

- **A third-party validator** — refused by ADR 0156.
- **Decoding into `any` and validating Go values** — `float64` loses
  `multipleOf` and large integers, and `encoding/json` accepts the duplicate
  names a schema author never meant.
- **Ignoring refused keywords** — a schema that relies on
  `unevaluatedProperties: false` would accept what its author refused.

## References

- [JSON Schema 2020-12 Core](https://json-schema.org/draft/2020-12/json-schema-core) and [Validation](https://json-schema.org/draft/2020-12/json-schema-validation).
- [JSON-Schema-Test-Suite](https://github.com/json-schema-org/JSON-Schema-Test-Suite).
