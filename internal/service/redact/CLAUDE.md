# internal/service/redact/

## Purpose

Redaction for DISPLAY (ADR 0101): a Go value, a JSON document, a text or a set
of log attributes, rendered with every secret the `Redactor` recognises replaced
by `[redacted]`, within an exact byte bound, never mutating the input. Public
facade: `pkg/v1/redact`.

Stdlib (`encoding/json` for the wire form of a value, `encoding/json/jsontext`
to stream a document, `reflect`, `regexp`) plus `internal/core/logger` for the
attribute shape. Code range `0.3.73.*`.

## Contents

| File | Role |
|---|---|
| `redact.go` | package doc, `Placeholder` / `Ellipsis` / `MinBytes`, `DefaultWords`, `Config`, `Redactor`, `NewRedactor`, `Name`, `bound` |
| `plan.go` | the per-type plan: which members of a type's JSON form are secret by declaration, walked the way `encoding/json` lays it out, cached per `Redactor` |
| `json.go` | `DocumentValue`, `JSON`, `Value`, and the `copier` — a `jsontext` token stream re-emitted by hand so every byte is accounted against the bound |
| `text.go` | `Text`, the URL-credential pattern, `clip` |
| `attrs.go` | `Attrs` — an iterator over `(dotted key, text)` pairs, `Unencodable`, and the per-kind rendering |
| `codes.go` / `errors.go` | `0.3.73.*`: `DocumentInvalid`, `ValueUnencodable` |

## Why-this-shape

- **A service package with no core counterpart.** Nothing here is a port: there
  is one engine and no second implementation a contract would describe, so the
  values are the engine's (ADR 0074) and the codes are the service's own. A
  `core/redact` would have held two sentinels and nothing else, which rule 5
  calls a stub.
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
- **A plan follows encoding/json's dominance rule.** For one member name the
  shallowest field is the one written, whatever the declaration order, so a
  deeper promoted field never replaces a shallower member's plan and a
  shallower one replaces a deeper one's; two fields at one depth keep the
  stricter plan, so a tie never loses a secret — `TestTheShallowestFieldDecides`.
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

## Verification

```sh
bazel test --config=race //internal/service/redact:redact_test
cd internal/service && GOWORK=off go test -race ./redact/
```
