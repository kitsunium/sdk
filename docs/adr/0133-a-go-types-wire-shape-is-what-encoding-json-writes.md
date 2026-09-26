# ADR 0133 — a Go type's wire shape is what encoding/json writes

- **Status**: Accepted
- **Date**: 2026-09-26
- **Deciders**: SDK maintainers
- **Related**: [ADR 0003](0003-sdk-codec-package.md) (the codec tree), [ADR 0102](0102-a-document-somebody-else-wrote-is-decoded-one-way-or-not-at-all.md) (a JSON package with its own facade), [ADR 0046](0046-sdk-validation-domain.md) (validation), [ADR 0101](0101-a-secret-shown-is-a-secret-replaced-and-the-bound-is-exact.md) (redact)

## Context

A framework documents its endpoints — their request and response bodies — from
the Go types themselves, in a developer console. It describes each type with a
walk of its own: a kind per Go kind, `time.Time` as a date-time string, a
pointer as nullable, fields named by their json tags. Read against what
`encoding/json` actually writes on go1.27.1:

- Every embedded struct was inlined and every name kept: two promoted fields of
  one name at one depth — which `encoding/json` writes NEITHER of — appeared
  twice, and a deeper field hidden by a shallower one appeared beside it.
- A struct embedding `time.Time` gains `time.Time`'s `MarshalJSON` and is
  written as a bare string; it was described as an object with a `Time`
  member.
- A field promoted through an embedded pointer is missing whenever the pointer
  is nil; it was described as always present.
- `,string`, a byte array (an array of numbers, not base64), `json.Number` (a
  number), a map whose key `encoding/json` refuses, a type reaching itself
  through a slice — each was described as something else, the last as an
  endless recursion.

And the rules themselves moved: Go 1.27 turns the `jsonv2` experiment on by
default, so the v1 API runs on json/v2's field walk with the legacy options.
Measured against `GOEXPERIMENT=nojsonv2`, the two engines agree everywhere but
four tag spellings: `json:"a'b"` (the engine writes `a`, the legacy one the Go
name), a control character in a name (kept, or the Go name), and the `embed`
option on a named struct field (promoted, or a member) or on a map (its entries
become members, or it is a member).

The framework must keep deciding, per field, whether a request field is read
from the path, the query, a header or a cookie — tags of its own.

## Decision

`internal/service/codec/jsonshape`, published as `pkg/v1/codec/jsonshape`:
`Of(reflect.Type) *Shape` and `For[T]() *Shape`. No codes: it never fails, and
what `encoding/json` refuses is described as `Unsupported` where it occurs.

### D1 — the home is the codec tree

The description is `encoding/json`'s — the wire form of a type under one
encoder — so it lives where the codecs live, beside `strictjson`, and not in
`validation`, whose only link is the `validate` tag a member carries as text
(`Field.Rules`). It has its own facade for `strictjson`'s reason (ADR 0102):
`pkg/v1/codec` blank-imports every codec, and describing a type needs none.

### D2 — the engine Go 1.27 builds `encoding/json` with

Members are resolved by json/v2's field walk under the v1 options, whose field
errors are ignored rather than reported: breadth first through embedded
structs, through a pointer, an unexported embedded struct's exported fields
included, a struct type met again lending its fields again (a diamond ties and
cancels) but not its embeddings (a cycle ends); for one name the shallowest
wins, a single tagged one breaks a tie at that depth, and a remaining tie
writes neither — nor any deeper field of that name. Tag names follow the
engine's grammar, and the `embed` option is honoured: a named struct field
carrying it is promoted, and an embedded map with string keys collects the
members no field names (`Shape.Values` on an Object).

### D3 — kinds as they are written

`time.Time` is a String with Format `date-time`, `time.Duration` an Integer
with Format `duration-ns`, `json.Number` a Number. A type writing its own JSON
(`json.Marshaler`, json/v2's `MarshalerTo`) is opaque, `Any`; one writing text
(`encoding.TextMarshaler`, `TextAppender`) is a String; either receiver counts,
since `encoding/json` calls a pointer receiver's method whenever the value is
addressable. A nil pointer, slice, map or interface is `null`, so those shapes
are Nullable; an array never is; `[]byte` is a base64 String and a byte array
an Array. A map whose key cannot be written as a member name — a bool, a
struct, a pointer-receiver text key — is Unsupported, as are channels,
functions and complex numbers.

### D4 — Optional is about encoding

A member is Optional when an encoded object may lack it: `omitzero`;
`omitempty` where the v1 definition of empty reaches (never a struct, an array
only of length zero); a field promoted through an embedded pointer. A pointer
field without either option is written as `null` — Nullable, not Optional.
`encoding/json` decodes any member as absent, so Optional never says what a
decoder requires. `Quoted` reports `,string` taking effect.

### D5 — each member keeps its Go field

`Field.GoName`, `Field.Tag` (the whole struct tag) and `Field.Index` (the path
through every embedding, for `FieldByIndex`). A framework reads its own tags
beside `encoding/json`'s; a field `encoding/json` never writes (`json:"-"`) has
no member, and `reflect.VisibleFields` — whose indexes are the same paths —
finds it. Recursion is a `Ref` to the enclosing type's qualified name; only a
named type can reach itself.

## Consequences

- The framework's `schema.go` becomes a mapping from `Shape` to its own model,
  plus its path/query/header/cookie reading over `Field.Tag`.
- A console now shows the members a client will actually receive.

## Breaking changes

None. `jsonshape` is a new package.

## Alternatives considered

- **`validation` as the home.** Its tag compiler re-derives the same key
  resolution, which argued for it; but the description is the encoder's, and a
  type described for an API page is not a type being validated.
- **Follow the legacy engine.** It is not what Go 1.27 builds; the four
  spellings above would be described as nothing writes them. A build with
  `GOEXPERIMENT=nojsonv2` — which cannot build `codec/strictjson` either — is
  described by the default engine's rules.
- **Describe JSON Schema.** A standard's vocabulary (`additionalProperties`,
  `nullable` or `null` in a type list, draft versions) the framework does not
  render; a consumer that wants one maps from `Shape`.
- **Import json/v2 to recognise `MarshalerTo`.** Matching the method by
  signature keeps the package building wherever `encoding/json` does.

## Deferred

- `internal/service/redact/fields.go` and `internal/service/validation/json_reach.go`
  each resolve the same members for their own purpose, both on the LEGACY
  engine's name rules (`isValidTag`, or a cut at the first comma) and without
  `embed`: measured, `json:"a'b"` is written `a`, which validation reads as
  the Go name and redact as `a'b`. Moving them onto this package's walk is a behaviour change in two other
  domains and is left to its own change.
- Map keys in the description (a key's Go kind, written as text).

## Verification

- `internal/service/codec/jsonshape/differential_external_test.go` —
  `json.Marshal` is the oracle: for each of sixteen structs (a shallow field
  hiding a promoted one, untagged and tagged ties, a tie hiding a deeper field,
  a diamond, promotion through a pointer and an unexported struct, an embedded
  non-struct, a named embedding, dashes, omissions over every kind, `,string`,
  the engine's names and `embed`, a collector), the populated value's members
  and order, the zero value's members against the non-Optional fields, each
  member's JSON kind, and the value at each `Index` against what was written
  under its name — which is what shows the right field won a tie.
- `shape_external_test.go` — every kind, opaque types against `json.Marshal`,
  map keys against `json.Marshal`, recursion through a slice, a pointer, a map,
  a pair of types and a pointer type naming itself, the Go field behind a
  promoted member, the shape's own JSON.
- `tag_internal_test.go` — every tricky tag, each name checked against the
  member `json.Marshal` writes.

## References

- [`encoding/json`](https://pkg.go.dev/encoding/json), [`encoding/json/v2`](https://pkg.go.dev/encoding/json/v2) — the field rules, the `embed` option.
- [`reflect.VisibleFields`](https://pkg.go.dev/reflect#VisibleFields)
