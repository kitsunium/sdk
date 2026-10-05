<!-- updated: 2026-10-05T00:00:00Z -->
# internal/service/security/redact/

## Purpose

Redaction for DISPLAY (ADR 0101): a Go value, a JSON document, a text or a set
of log attributes, rendered with every secret the `Redactor` recognises replaced
by `[redacted]`, within an exact byte bound, never mutating the input. The
engine behind the `internal/core/security/redact` port (ADR 0160): `*Redactor`
implements `coreredact.Redactor`, asserted at compile time in
`decl_gen.go`, where kit writes the design's `implements:` (ADR 0170). Public facade:
`pkg/v1/security/redact`.

Stdlib (`encoding/json` for the wire form of a value, `encoding/json/jsontext`
to stream a document, `reflect`, `regexp`) plus `internal/core/observe/logger` for the
attribute shape, `internal/core/security/redact` for the port, its values and
its codes, and `internal/kernel/errs`. Codes: none declared here — the domain's
`0.3.73.*` range was allocated here (ADR 0101) and is declared in the core
since ADR 0160, with its values unchanged; this package raises it.

## Contents

| File | Role |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `decl_gen.go` | written by kit gen from the design (ADR 0170): the declarations of `Config` and `Redactor` — each struct with every field, unexported ones included; `Redactor.Attrs`, each one call of its unexported body, measured to inline with the body inlined into it; the assertion `Redactor → coreredact.Redactor`. Every body stays hand-written, in the files this document names — each wrapper's under its unexported name |
| `redact.go` | `DefaultWords`, `Config`, `Redactor`, `NewRedactor`, `Name`, `bound` |
| `plan.go` | the per-type plan: which members of a type's JSON form are secret by declaration, cached per `Redactor` |
| `fields.go` | `writtenFields` — for a struct, the one field `encoding/json` writes under each member name, selected by `encoding/json`'s own rules (`jsonName`, level-by-level embedding, `dominant`) |
| `json.go` | `JSON`, `Value` (both returning the core's `DocumentValue`), and the `copier` — a `jsontext` token stream re-emitted by hand so every byte is accounted against the bound |
| `text.go` | `Text`, the URL-credential pattern (`credentials`, compiled at the first text holding "://" and "@", not at every program start), `clip` |
| `attrs.go` | `Attrs` — an iterator over `(dotted key, text)` pairs, and the per-kind rendering |

## Why-this-shape

- **The port and the values are the core's; the engine and its configuration
  are here.** ADR 0101 §D5 first shipped this as one engine with no core;
  ADR 0160 gave every service domain a core, so `internal/core/security/redact`
  now holds the `Redactor` port (frozen at `Name` / `Text` / `JSON` / `Value` /
  `Attrs`, so a consumer can hand a test double where a redactor is expected),
  the values every implementation writes and a caller compares against
  (`Placeholder`, `Ellipsis`, `MinBytes`, `Unencodable`, `DocumentValue`) and
  the two codes. `Config` and `DefaultWords` stay here: they are this engine's
  construction parameters, which a second implementation would not have to
  accept (ADR 0074).
- **The configuration is the policy, and it is small.** Words (defaulting to
  ten fragments; an empty list means the defaults, never "none" — a redactor
  that recognises no name is a formatter), a tag key (default `redact`, so a
  framework keeps `kit:"secret"` by passing `Tag: "kit"`), an extra field rule
  (a framework's own binding tags — a cookie-bound field, a header named like a
  secret), and how an error is shown. A plan depends on the tag and the rule,
  so the cache belongs to the `Redactor`, not to the package.
- **The bound is EXACT, and that is why the copier writes JSON itself.**
  `jsontext.Encoder` inserts its own separators and chooses its own escaping,
  so the size of the next write cannot be known before it is made; the
  downstream copy built on it checked the bound between tokens and could
  overshoot it by up to 64 bytes of a string, a number of any length, and every
  closer. Here the reader is `jsontext.Decoder` and the writer is this
  package: every token's cost — separator, escaped name, colon, the smallest
  value a cut can still write, one closing byte per open container — is
  checked before it is written. `TestTheBoundIsExactAndTheOutputAlwaysWellFormed`
  sweeps every bound from the floor to past the document's length and asserts
  `len <= bound` and `json.Valid` at each one. `escapedLength` and
  `appendEscaped` must agree byte for byte; `Test_escapedLength` pins it.
- **A cut is a prefix.** Once anything is left out nothing more is written —
  a later, smaller member is not squeezed in behind a hole — and the input is
  still read to its end, so a document malformed after the cut is still
  refused.
- **Scrub, then cut.** `Text` replaces URL credentials BEFORE it cuts; the
  other order can cut `https://admin:hunter2@db` at `hun` and show it, because
  without its `@` a prefix is not recognisable as a password. The downstream
  copy this replaces cut first.
- **The URL pattern is greedy to the LAST `@`.** `https://user:p@ss@host` is one
  userinfo with an unescaped `@`; stopping at the first would show `ss`.
  Over-redacting a host that happens to contain `@` before its path is the safe
  direction.
- **`json:"-,"` is a member named `-`.** The whole tag is compared with `-`, as
  `encoding/json` does; the downstream copy compared the name, so a secret field
  spelled that way lost its declaration.
- **A malformed document is refused, not shown as a fragment.** A partial copy
  of something that does not parse cannot be trusted to have had its secrets
  recognised.
- **A plan applies to the field encoding/json WROTE, selected by its own
  rules.** `Value` marshals with `encoding/json` and redacts the result by
  member name, so the plan must pick, for each name, the very field
  `json.Marshal` picked: embedded structs expanded one level at a time and each
  type once; the shallowest fields of a name compete; of several, a single
  tagged one wins, otherwise none is written; a type embedded twice at one level
  cancels out; an embedded struct is flattened by its kind, even one that writes
  its own JSON when two such sit side by side; an unexported embedded struct
  WITH a json name is written whole under it. An earlier approximation — "the
  stricter plan wins a tie", promotion refused to self-marshalling structs, an
  unexported named embedding ignored — let a declared secret through in all
  three cases (`TestThePlanFollowsTheFieldEncodingJSONWrites`, each case
  checked against `json.Marshal`'s own output; `TestTheShallowestFieldDecides`).
- **Every text `Attrs` hands out is cut**, a timestamp or a number as much as a
  string — `TestAttrsBoundEveryKind`; `TestDeepNestingStaysWithinTheBound` pins
  the copier against a document that is all structure.
- **`Attrs` is an iterator** so the caller bounds the COUNT: a record carrying a
  group of ten thousand attributes costs exactly what is ranged over. A group
  whose dotted key is a secret's is ONE redacted pair.

## Do NOT

- Encode through `jsontext.Encoder` or `json.Marshal` on the way OUT. The bound
  is exact only because every byte written is counted before it is written.
- Keep a document's bytes in an error, a Private or a field.
- Mutate a value, a document or an attribute slice. Everything here reads.
- Add a "no words" configuration. It is a formatter with a misleading name.
- Declare an error code, or a value the port speaks, here. The codes, the
  `Redactor` port and the values a caller compares results against are
  `internal/core/security/redact`'s (ADR 0160); a new refusal gets its code and
  sentinel there, in the range this package already owns (`0.3.73.*`).

## Verification

```sh
bazel test --config=race //internal/service/security/redact:redact_test
cd internal/service && GOWORK=off go test -race ./security/redact/
```
