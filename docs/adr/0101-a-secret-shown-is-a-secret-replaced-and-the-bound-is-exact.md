# ADR 0101 — a secret shown is a secret replaced, and the bound is exact

- **Status**: Accepted
- **Date**: 2026-09-25
- **Deciders**: SDK maintainers
- **Related**: [ADR 0074](0074-what-a-public-alias-may-point-at.md) (whose type a value is), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (zero values), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) (the code range)

## Context

Anything that shows a program's data to a person — a trace viewer's request
and response payloads, a log panel, a store browser in a developer console —
has to keep the secrets in that data off the screen. A downstream framework
wrote its own engine for it, about 350 lines, and its shape is general: a
member whose NAME says it is a secret ("password", "accessToken",
"X-Api-Key"), a struct field DECLARED secret by a tag, and the CREDENTIALS in a
URL, each replaced; the result cut to a size a screen can hold; the input never
modified. Three defects were found in reading it:

- **The cut came before the scrub.** A log message was clipped to its bound
  and THEN had its URL credentials replaced, so a clip landing inside
  `https://admin:hunter2@db` removed the `@` and with it everything that marked
  `hunter2` as a password: it was shown.
- **The bound was approximate.** The copy checked its size between tokens and
  clipped a string to at least 64 bytes, so a result could exceed its bound by
  up to 64 bytes of a string, by a number of any length, and by every closer
  still owed.
- **`json:"-,"` was read as "never written".** encoding/json compares the whole
  tag with `-`; a field tagged `-,` is written as a member named `-`. The copy
  compared the name before the comma, dropped the field from its plan, and a
  secret declared on it was shown.

And the URL pattern stopped at the FIRST `@`, so `https://user:p@ss@host` —
an unescaped `@` in a password, which people do write — showed `ss`.

## Decision

A new service package, `internal/service/redact`, published as
`pkg/v1/redact`. Code range `0.3.73.*` (`0x00_03_49_*`), two codes.

### D1 — a `Redactor` built from a small policy

`NewRedactor(Config{Words, Tag, Field, Error})`:

- **Words** — fragments of a name that make it a secret, matched
  case-insensitively anywhere in it. Nil or empty means the ten defaults
  (`password`, `passwd`, `secret`, `token`, `authorization`, `cookie`,
  `session`, `apikey`, `api_key`, `api-key`); there is no way to recognise NO
  name, because a redactor that recognises no name is a formatter.
- **Tag** — the struct tag key whose options, when one is `secret`, declare a
  field secret. Empty means `redact`, so `redact:"secret"` is the SDK's
  spelling and a framework keeps its own by passing `Tag: "kit"`.
- **Field** — an extra rule over `reflect.StructField`, for what a framework
  declares by other means: a field bound to a cookie, a field bound to a header
  whose name is a secret's.
- **Error** — how an error inside a log attribute is shown; nil is its own
  text, scrubbed and cut.

The per-type plan depends on the tag and the field rule, so the cache is the
`Redactor`'s, not the package's.

### D2 — five operations, one bound each

`Name(name)`, `Text(s, maxBytes)`, `JSON(document, maxBytes)`,
`Value(v, maxBytes)`, `Attrs(attrs, maxBytes)`:

- `Value` encodes with `encoding/json` — the wire form — and judges the result
  by names AND by the declarations found walking `v`'s type the way
  encoding/json lays it out, through pointers, slices, maps and promoted
  embedded structs, once per type, recursive types sharing their own plan. A
  type that writes its own JSON is opaque to the walk.
- `JSON` judges a document by names only. A member whose name is a secret's is
  replaced whole, object or not.
- Every string, in both, has its URL credentials replaced: the scheme and the
  host stay, the userinfo up to the LAST `@` before the path goes.
- `Attrs` renders log attributes as `(dotted key, text)` pairs through an
  ITERATOR, so the caller bounds the count by breaking; a group whose dotted key
  is a secret's is ONE redacted pair.

### D3 — the bound is exact, so the writer is this package's

A JSON result is never longer than its bound and is always one well-formed
value. `jsontext.Encoder` inserts its own separators and chooses its own
escaping, so the size of a write cannot be known before it is made; the reader
is `jsontext.Decoder`, and the writer is ours. Before each token the copier
checks its separator, its escaped length, and — for a member — the colon and
the smallest value a cut can still write (`"…"`, five bytes), with one closing
byte kept aside for every container still open. A container that does not fit
is closed early; a string is cut at a rune and ends in `…`; a number, which
cannot be cut, becomes `"…"`. Once anything is left out nothing more is written
— a cut is a prefix, not a document with holes — but the input is still read to
its end, so a document malformed after the cut is still refused.
`Truncated` says whether anything was left out.

A bound below `MinBytes` (16) is RAISED to it, not refused: the floor is the
smallest output that can hold its own marker and closers, a display bound is a
size and not a policy, and showing less than asked is the safe direction of a
wrong number (ADR 0031's clamp).

### D4 — scrub, then cut; refuse what does not parse

`Text` replaces credentials BEFORE it cuts, so a cut only ever shortens what is
already safe. `JSON` refuses a document that is not exactly one JSON value
(`DocumentInvalid`, `0.3.73.1`) and returns nothing for it — a partial copy of
something that does not parse cannot be trusted to have had its secrets
recognised; `Value` refuses what encoding/json will not encode
(`ValueUnencodable`, `0.3.73.2`), naming the Go type and never the value.
Duplicate member names and invalid UTF-8 are tolerated, invalid bytes shown as
U+FFFD: this is for showing a document, not for accepting one.

### D5 — a service package with no core counterpart

Nothing here is a port: one engine, no second implementation a contract would
describe. The values are the engine's (ADR 0074) and the codes the service's
own. A `core/redact` would hold two sentinels and nothing else, which is the
stub rule 5 forbids.

## Consequences

- The framework can delete `redact.go` and the attribute rendering of
  `logs.go` (`addLogAttr`, `anyText`, `clipText`, `scrubText`) and keep its
  policy in a `Config`: `Tag: "kit"`, a `Field` rule for its `cookie` and
  `header` binding tags, and an `Error` renderer that shows its own wire text.
- What is NOT recognised is shown, and the package doc says so first: a
  password in a member called `p`, a key pasted into free text, a secret in a
  map keyed by something innocent. This is a display filter, not an access
  control, and not a sanitiser for data that goes back into a system.
- Measured cost is not claimed; the operations run where a person will read
  the result, and the plan is built once per type.

## Breaking changes

None. `redact` is a new package in this change set.

## Alternatives considered

- **Build on `jsontext.Encoder`.** Its separators and escaping are its own, so
  the bound can only be checked after a write — the overshoot the downstream
  copy had.
- **Redact the Go value by reflection, without encoding it.** It would have to
  re-implement every encoding/json rule — `omitempty`, `string`, embedded
  pointers, `MarshalJSON`, `TextMarshaler` — to show what the wire shows.
  Encoding once and redacting the tokens shows exactly the wire form.
- **A `core/redact` port.** Nothing implements it twice, and a logger or trace
  middleware that later wants redaction can take a `*Redactor` or a narrow
  interface of its own; declaring one now would be a contract describing one
  engine.
- **Refuse a non-positive bound.** It is a display size; raising it to the
  floor shows less, which is the safe way to be wrong.

## Deferred

- **Query-string secrets** (`?token=…`, `&password=…`) in free text. A second
  rule over text is plausible; the word list would have to apply to parameter
  names, and none of the three rules needs it to be right.
- **A logger middleware** that redacts attributes before any sink sees them.
  The SDK logger renders `Any` as `?` today, so there is nothing to redact on
  its wire; the day it renders values, this is what it composes.

## Verification

- `internal/service/redact/redact_external_test.go` —
  `TestValueReplacesEverySecretItRecognises` is the framework's own acceptance
  test carried over (names, a tag with two options, a pointer, a slice, a map,
  an embedding, the caller's cookie and header rule) plus "the input is
  unchanged"; `TestTheBoundIsExactAndTheOutputAlwaysWellFormed` sweeps every
  bound from the floor to past the document and asserts `len <= bound`,
  `json.Valid` and the `Truncated` flag at each; `TestTextScrubsBeforeItCuts`
  holds the cut-inside-the-credentials case and the `@`-in-password case.
- `internal/service/redact/redact_internal_test.go` — `escapedLength` and
  `appendEscaped` agree byte for byte on every character class, `json:"-,"`,
  and the recursive-type plan.
- `internal/service/redact/attrs_external_test.go` — every attribute kind, a
  secret group as one pair, `Config.Error`, the iterator stopping, the per-text
  bound.

## References

- [`encoding/json`](https://pkg.go.dev/encoding/json) — the layout the plan follows, and the whole-tag `-` rule.
- [`encoding/json/jsontext`](https://pkg.go.dev/encoding/json/jsontext) — the reader.
- [RFC 8259 §7](https://www.rfc-editor.org/rfc/rfc8259#section-7) — which characters a JSON string must escape.
- [RFC 3986 §3.2.1](https://www.rfc-editor.org/rfc/rfc3986#section-3.2.1) — userinfo, and why an unescaped `@` in it is common in practice and wrong in theory.
