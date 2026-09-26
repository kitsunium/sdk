<!-- updated: 2026-09-26T00:00:00Z -->
# internal/service/codec/jsonshape/

## Purpose

Describes how values of a Go type look on the wire under `encoding/json`: the
JSON kind of each value, an object's members in the order they are written,
which may be missing, which may be null, which travel quoted — and, for each
member, the Go field behind it (tag, Go name, index path) (ADR 0133). Public
facade: `pkg/v1/codec/jsonshape`.

Stdlib only; not a codec — it registers no Format and encodes nothing. It
imports `encoding/json` for three types and two interfaces, and recognises
json/v2's streaming methods by signature so it does not import json/v2.

## Contents

| File | Role |
|---|---|
| `shape.go` | package doc, `Kind` and its nine values (`String`, `MarshalText`), `ShapeValue`, `FieldValue`, `Of`, `For` |
| `walk.go` | the walker: the path of named types being described (recursion becomes a `Ref`), pointers, the three known types, opaque types, the kinds, slices (base64 or array), maps (`keyWritable`), structs |
| `fields.go` | `members` — json/v2's struct field walk under the v1 options, breadth first; `embed` (promote, collect, or a member after all); `writable`; `dominantMembers`; `dominantFallback`; `optional`, `quoted`, `emptiable`, `throughPointer` |
| `tag.go` | `parseTag` — the json tag grammar the engine reads: names up to a reserved character, identifiers, `case:` and `format:` values, single-quoted strings |
| `methods.go` | which methods hand a type's encoding to the type: `writesJSON`, `writesText`, `hasAnyMethod`, `isJSONTextValue` |

## Why-this-shape

- **The engine is the one Go 1.27 builds `encoding/json` with.** Go 1.27 turns
  the `jsonv2` experiment on by default, so the v1 API runs on json/v2's
  `makeStructFields` with the legacy options — whose field errors are IGNORED,
  not reported. That walk is what this package reproduces. Measured on
  go1.27.1 against `GOEXPERIMENT=nojsonv2`, the two engines agree everywhere
  except four tag spellings: a name holding `'` or a control character (the
  engine keeps the identifier before a quote, and a control character as
  written; the legacy one falls back to the Go name), and the `embed` option on
  a named struct field or a map (promoted or collecting; the legacy one ignores
  it). The suite's oracle is `json.Marshal` of the build under test.
- **Promotion is resolved, not approximated.** Breadth first through embedded
  structs, a struct type met again lending its fields again (so a diamond ties
  and cancels) but not its embeddings (so a cycle ends); for one name the
  shallowest wins, a single tagged one breaks a tie at that depth, a remaining
  tie writes neither — nor any deeper field of that name. The copy it replaces
  inlined every embedded struct and kept every name.
- **Optional is about encoding.** `omitzero`; `omitempty` only where the v1
  definition of empty reaches (never a struct, an array only of length zero);
  and every field promoted through an embedded POINTER, which a nil pointer
  drops. A pointer field without either option is written as `null`, so it is
  Nullable, not Optional.
- **Opaque means the type decides.** `json.Marshaler` / json/v2's `MarshalerTo`
  → Any; `encoding.TextMarshaler` / `TextAppender` → String; either receiver
  counts. A struct embedding `time.Time` gains its method and is opaque — the
  copy it replaces described it as an object with a `Time` member.
  `json.Number` has a `MarshalJSONTo` under this engine and is special-cased:
  a Number, quoted under `,string` because its own method honours the option.
- **A field keeps its Go field.** `Tag`, `GoName` and `Index` (the full path for
  `FieldByIndex`) are what let a framework read its own tags beside
  encoding/json's. A field json never writes has no member; `reflect.VisibleFields`
  finds it and the index path joins the two.
- **Recursion is a reference.** Only a named type can reach itself, so named
  types go on the walk's path; met again, the type is described as a `Ref` with
  no members. A pointer type naming itself is its own element, so its shape IS
  the reference.
- **Every call is a new tree.** No cache: a caller may keep and edit what it is
  given, and a description is built once per endpoint at startup.

## Error range

None. `Of` never fails: what `encoding/json` refuses is described as
`Unsupported` where it occurs.

## Do NOT

- Split a tag on its first comma: a quoted `format:` value holds commas, and a
  name stops at a reserved character before any comma.
- Describe an embedded struct by its kind without asking whether the OUTER type
  gained a marshaler through it.
- Treat a pointer field as optional: it is written as `null`.
- Import `encoding/json/v2` here: the streaming methods are matched by signature
  so the package builds wherever `encoding/json` does.

## Verification

```
bazel test --config=race //internal/service/codec/jsonshape:jsonshape_test
cd internal/service && GOWORK=off go test -race ./codec/jsonshape/
```

`TestShapesMatchWhatEncodingJSONWrites` is the oracle test: for every case, the
populated value's members and order, the zero value's members against the
non-Optional fields, each member's JSON kind, and the value at each `Index`
against what was written under its name. `TestParseTagReadsWhatTheEngineReads`
checks every tricky tag's name against the member `json.Marshal` writes.

## Reference

- ADR 0133 — `docs/adr/0133-a-go-types-wire-shape-is-what-encoding-json-writes.md`
- `internal/service/redact/fields.go` and `internal/service/validation/json_reach.go`
  resolve the same members for their own purposes; ADR 0133 §Deferred says why
  they are not moved onto this package here.
