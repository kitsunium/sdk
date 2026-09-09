<!-- updated: 2026-09-09T00:00:00Z -->
# internal/service/codec/form/

## Purpose

`application/x-www-form-urlencoded` — the encoding every HTML form POSTs and
every query string carries. Pairs of percent-escaped `key=value` joined by
`&`, with space written as `+`. Stdlib-only: `net/url` supplies the decode
half, the encode half is hand-rolled so `Append` can write into a caller-owned
buffer.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"form"` |
| `MIMETypes()`    | `application/x-www-form-urlencoded` |
| `Extensions()`   | `.form`, `.urlencoded` |
| Constructor      | `New() codec.Codec` |
| Streaming        | not implemented — the format has no record framing; a body is one unit |
| Appender         | yes (`Append(dst, v) ([]byte, error)`) — dst is returned untouched on rejection |

Accepted shapes, both directions:

| Go shape | Marshal / Append | Unmarshal target | Fidelity |
|---|---|---|---|
| `url.Values`          | yes | `*url.Values`          | full |
| `map[string][]string` | yes | `*map[string][]string` | full |
| `map[string]string`   | yes | `*map[string]string`   | encode: lossless · decode: **refuses** a repeated key |

## The decision that matters: repeated keys and arrays

`a=1&a=2` is where every implementation of this format diverges. The choice
here, and why:

**A key may repeat, and repetition is the only array syntax.** `a=1&a=2`
decodes to `{"a": ["1", "2"]}`, in wire order. `Marshal(url.Values{"a":
{"1","2"}})` emits `a=1&a=2`.

- **Not last-wins / first-wins.** Both silently discard a value the wire
  actually carried. A codec whose contract is `Marshal`/`Unmarshal` cannot
  drop data and report success.
- **Not `a[]=1&a[]=2`, `a[0]=1`, or comma-joining.** None of these is in the
  format: they are framework dialects (PHP and Rails spell it `a[]`, `qs`
  spells it `a[0]`, ASP.NET comma-joins) and no two agree. Comma-joining is
  additionally ambiguous the moment a value legitimately contains a comma.
  Inventing a dialect here would make `form` mean something different in this
  SDK than in the HTTP stack next to it.
- **It is what `net/url.ParseQuery` does.** That matters concretely: an
  application that reads a body with `codec.Unmarshal(codec.Form, …)` and one
  that reads it with `r.ParseForm()` must not disagree inside the same
  process.

Hence the native Go shape is `url.Values` (= `map[string][]string`), not a
struct and not `map[string]string`.

### What round-trips, and what does not

`Unmarshal ∘ Marshal` is the identity on `url.Values`, with exactly one
exception:

- **A key bound to an empty (or nil) slice emits no bytes and comes back
  absent.** `url.Values{"a": {}}` encodes to `""`. The wire has no way to say
  "key present, zero values", so this is the one input the encoding cannot
  represent. It is documented rather than papered over.

`Marshal ∘ Unmarshal` is the identity on *values*, and on *bytes* only when
the input is already canonical. Canonical means: keys in ascending
lexicographic order, every pair spelled `k=v`, space as `+`, uppercase `%XX`,
no trailing `&`. Non-canonical inputs are normalised, not preserved:

| Input | Decoded | Re-encoded |
|---|---|---|
| `b=2&a=1` | `{"a":["1"],"b":["2"]}` | `a=1&b=2` |
| `a`       | `{"a":[""]}`            | `a=`      |
| `a=%2f`   | `{"a":["/"]}`           | `a=%2F`   |
| `a=1&`    | `{"a":["1"]}`           | `a=1`     |

Key order is sorted because Go map iteration is randomised: without a sort the
same value would encode to different bytes on different runs, which would make
every content hash, cache key and golden test downstream nondeterministic.
`TestRoundTripContract` pins each row above, and additionally asserts that the
**second** round-trip is always a byte-level fixpoint.

### Struct targets go through the facade, and it shows

This codec does not map Go structs onto form fields. There is no agreed
convention for nested structs, slices of structs, or optional fields in
urlencoded, and inventing one would be a private dialect (see above).
`pkg/v1/codec.Marshal(codec.Form, someStruct)` therefore takes the facade's
JSON-bridge promotion path and produces a **single pair**,
`_json=%7B…%7D` — a transport wrapper, not a field mapping. Callers who want
`name=Ada&age=36` build the `url.Values` themselves.

## Error codes (range `0.3.40.*`)

| Code        | Var               | Trigger |
|---|---|---|
| `0.3.40.1`  | `ValueInvalid`    | argument / target is not one of the three modelled shapes (or is a nil pointer to one) |
| `0.3.40.2`  | `UnmarshalFailed` | `net/url.ParseQuery` refused the input (bad `%XX` escape, `;` separator) **or** a decode bound was exceeded |
| `0.3.40.3`  | `MultiValue`      | decode of a repeated key into a `map[string]string` target |

**There is deliberately no `MarshalFailed`.** Past the argument-shape gate,
percent-escaping a map of strings is a total function — no byte sequence makes
it fail. A sentinel no test can ever provoke would be a lie in the registry.

`MULTI_VALUE` is intentionally *not* one of the reasons
`pkg/v1/codec`'s `isValueShapeMismatch` treats as promotable: it is a fidelity
refusal, not a shape mismatch, so it reaches the caller verbatim instead of
being retried through the JSON bridge and re-labelled `PROMOTE_FAILED`.

## Conventions

- **Decode bounds are compile-time constants, not constructor options.**
  `maxFormBytes` (8 MiB) and `maxFormPairs` (10 000) are `const`. ADR 0031
  forbids a policy whose zero value is silently inert, and a
  `NewWithLimit(0)` would be exactly that — a caller passing the zero value
  would disable the guard while believing they configured it. With constants
  there is no zero value to misread; raising a bound is a reviewed source
  change. The pair ceiling is checked from the `'&'` count **before**
  parsing, so an over-long body is refused without allocating a map
  (`url.ParseQuery` has no pair limit of its own).
- **`net/url` owns the decode half.** `'+'` → space, `%XX` folding and the
  post-Go-1.17 refusal of `;` as a separator are stdlib behaviour, not
  re-implemented here. Re-implementing them is how a form decoder drifts from
  `r.ParseForm()`.
- **The encode half is hand-rolled, and pinned to the stdlib.**
  `appendQueryEscape` exists only because `url.QueryEscape` returns a
  `string`, which would cost one allocation per key and per value.
  `TestAppendQueryEscapeMatchesStdlib` compares it against `url.QueryEscape`
  over **all 256 byte values** plus mixtures, and
  `TestEncodeIntoMatchesStdlibEncode` compares the whole encoder against
  `url.Values.Encode()`. The moment they disagree this is a different format.
- **The output buffer is grown exactly once.** `encodedLen` predicts the byte
  count to the byte; `TestEncodedLenIsExact` / `TestEscapedLenIsExact` keep the
  prediction honest, since an under-estimate would silently restore the
  geometric append cascade the single `Grow` exists to avoid.
- **A repeated key reported by `MULTI_VALUE` is the lexicographically
  smallest one**, not the first the map iterator happened to yield — an error
  message that changes between runs is not a diagnostic.
- **Neither file extension is standardised.** urlencoded is a wire encoding
  for HTTP bodies and query strings, not a file format. `.form` /
  `.urlencoded` exist so `codec.LookupExt` has a deterministic answer for
  callers who persist a captured body.

## Do NOT

- Add bracket-array parsing (`a[]=`, `a[0]=`) or comma-splitting. See above:
  those are framework dialects, and adopting one silently breaks every caller
  whose peer picked a different one.
- Make `map[string]string` decoding "just work" on a repeated key by picking
  a winner. The refusal is the feature.
- Add struct↔form field mapping here. If it is ever wanted, it is a design
  decision with an ADR, not a helper.
- Turn the decode bounds into constructor parameters without reading ADR 0031
  first.

## Verification

```
bazel test --config=race //internal/service/codec/form:form_test

# Fallback (no Bazel)
cd internal/service && GOWORK=off go test ./codec/form/...
```

`codec_integration_test.go` carries `//go:build !race` (`testing.AllocsPerRun`
counts one extra allocation under the race detector) and is therefore invisible
to the race suite. Its compensating lane is the race-off alloc lane, which
already covers this package through the `//internal/service/codec/...` entry in
`tools/alloc-lane-targets.txt` — CLAUDE.md rule 12 wants that lane named in the
same change, and this is the naming.
