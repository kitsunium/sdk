# internal/kernel/jsonenc/

## Purpose

**One JSON string escaper for the SDK's hand-written emitters**, and the
small things that go with it: an integer appender, a float appender with
encoding/json's spelling, and an ordered-key object builder. Stdlib-only AND generic — a slice, a string, a number; no logger, no
token, no codec in a signature — so it passes SDK rule 1. It **registers no
codec** and reads nothing by reflection: `codec/json` is still `encoding/json`,
and `codec/json.Marshal` does not change.

It exists because the SDK carried four copies of "write this string as JSON"
that disagreed (ADR 0186 §D10): the logger's byte loop, the OTLP emitters'
rune loop, `redact`'s rune loop and the token formats' byte loop. They differed
on `\b` and `\f`, on invalid UTF-8 and on U+2028/U+2029, and every one of them
paid a per-byte test for the plain ASCII that is nearly every string.

**Published** through the codec domain, not on its own (ADR 0186 §D10, which
amends ADR 0159 §4): `service/data/codec/json` and `pkg/v1/data/codec/json`
re-export `AppendString`, `AppendEscaped`, `AppendInt`, `AppendUint`,
`AppendFloat`, `BeginObject` and `Object`, as the logger publishes `buffer`. Emits **no error
codes**, so the package needs no `PP` range, no `codeRangeOwners` row and no
`//:audit_sources` label.

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `string.go` | `AppendString`, `AppendEscaped`, `AppendInt`, `AppendUint`; the search `needsAttention`, the byte loops `appendShort` and `appendScalar`, the table `plain`, the byte escape `appendEscapedByte` |
| `float.go` | `AppendFloat` — encoding/json's float64 spelling (#1039); `trimExponentZero` |
| `object.go` | `Object`, `BeginObject` and its members (`String`, `Int`, `Uint`, `Float`, `Bool`, `Null`, `Raw`, `End`) |
| `gen_api_test.go`, `gen_perf_test.go` | kit's pins and the allocation budgets (design/sdk.yaml) |

## What a string becomes

| Input | Output | Why |
|---|---|---|
| `"` `\` | `\"` `\\` | RFC 8259 §7 |
| `\b \f \n \r \t` | the short escapes | what `encoding/json` writes since Go 1.22; the logger's byte loop wrote `\u0008`, `\u000c` |
| other byte < 0x20 | `\u00xx`, lowercase | RFC 8259 §7 |
| U+2028, U+2029 | ` `, ` ` | legal JSON, a line end in a script; `encoding/json` escapes them |
| invalid UTF-8 | U+FFFD per bad byte, written as its three bytes | the output is always valid UTF-8; `encoding/json` of Go 1.27 writes it the same way |
| DEL (0x7f), `<` `>` `&` | as they are | **no HTML escaping**, as `json.Encoder.SetEscapeHTML(false)` |

`json.Marshal` escapes `<`, `>` and `&` (as `\u003c` and so on); this package does not, as `json.Encoder` with `SetEscapeHTML(false)` does not. A caller that embeds the result in HTML escapes it itself.

## What a float becomes

`AppendFloat` writes what `json.Marshal(float64)` writes (encode.go,
`floatEncoder`, the ES6 number-to-string rule): the shortest round-trip digits,
plain from 1e-6 up to 1e21 and exponent notation outside it, and a negative
one-digit exponent without its leading zero. So `1e6` is `1000000` (strconv's
`'g'` writes `1e+06`) and `1e-7` is `1e-7` (`'g'`: `1e-07`); `1e21` is `1e+21`,
`-0` is `-0`. NaN and ±Inf have no JSON number: it appends nothing and returns
`ok == false`, and each emitter spells its own policy on that branch — the
logger's JSON encoder writes `null`, OTLP the proto3 strings `"NaN"`,
`"Infinity"`, `"-Infinity"`; `Object.Float` writes `null`. RFC 8785's
canonical form (`service/data/codec/jcs`) writes its numbers here too — ES6's
rule is its rule — and keeps one case of its own: `-0` is `0` there. The
emitters that are NOT JSON keep their own formatting: trace's `formatFloat`,
`redact`'s text rendering, the Prometheus exporter, the logger's TEXT encoder.

## Why-this-shape

- **A byte loop below 32 bytes, a word search above it, and the loop again
  where the text is dense.** The search (`swar.NewSet('"', '\\', 0x20, true).Index`)
  finds the next byte that needs attention a word at a time and everything before
  it is one `append`; but a call costs a copy of the set and a dispatch, which a
  26-byte value never earns back, so a string under `searchMin` is read by
  `appendShort` (a table lookup per byte, one copy when nothing needs escaping).
  Past it, a hit hands the next `scalarWindow` bytes to the byte loop
  (`appendScalar`), and while that window finds a second hit the loop keeps
  reading: text full of escapes or of non-ASCII has one every few bytes, and
  a search call per hit measured slower than the loop it replaced (BENCH.md).
  The set includes bit 7, so non-ASCII text is always in the loop's hands, and
  `appendScalar` validates it: two- and three-byte runes whose lead rules out an
  overlong form or a surrogate are read in place, everything else (four-byte
  runes, E0, ED, bad bytes, the two separators) goes through `DecodeRuneInString`.
  This package never walks a search mask (the borrowing form's fault).
- **`unsafe.StringData` for the search, and only for it.** `swar` takes a
  `[]byte`; converting a string allocates past the compiler's small buffer. The
  view is read-only, built only for a non-empty string (the data pointer of an
  empty one is unspecified), and never escapes this file. `swar` itself has no
  `unsafe`; this package has the one line.
- **Invalid UTF-8 is repaired here, and refused by the caller that must.** The
  token issuer calls `checkClaimText` before it renders anything, because a
  repair would mint `"a\xff"` and `"a\xfe"` as two tokens that verify as one
  subject. `jsonenc` does not know that; it keeps its output valid.
- **`Object` is a value.** Two words, returned by every member; nothing escapes
  and the only allocation is the caller's slice growing. It does not sort, does
  not check duplicates and does not nest: `Raw` takes a value another `Object`
  built. Keys are escaped like any string.
- **Not a codec.** Anything that wants a struct on the wire still wants
  `encoding/json`. This package is for the emitters that already write bytes.

## Tests

| File | What it pins |
|---|---|
| `jsonenc_external_test.go` | the exact bytes for every class of input; every byte value at every offset of a 40-byte string; every pair of bytes between plain text; every lead byte C0 to FF with the continuation and near-continuation values after it, up to four bytes, in a short string and between two long runs (the window and the in-place reads); the differential against `encoding/json` (valid JSON, valid UTF-8, no raw separator, decodes to what `Marshal` decodes to, and byte-for-byte equal to `Encoder` with HTML escaping off for valid input); `FuzzAppendString` (`go test -fuzz FuzzAppendString`); the object; the examples |
| `float_external_test.go` | `AppendFloat` against `json.Marshal` over an edge corpus (±0, subnormals, the 1e-6 and 1e21 cut-offs and their neighbours, 2^53, the extremes), byte for byte and read back to the same bits; the non-finite refusal; `Object.Float`; `FuzzAppendFloat` over every bit pattern (`go test -fuzz FuzzAppendFloat`) |
| `perf_fixtures_test.go`, `gen_perf_test.go` | the design's budgets: `allocs: 0` for every appender, race off (the alloc lane) |
| `bench_test.go` | the figures `BENCH.md` records, against the byte loop and `encoding/json` |

**MUTATIONS** (each failed the suite when applied): dropping the `0xED`
exclusion from the in-place three-byte read (a surrogate passed) failed
`TestAppendStringExact` and `TestMultibyteSequences`; comparing `s[i+2]` to
`0xA8` alone (U+2029 passed unescaped) failed the same two; building the search
without the upper half failed `TestPositionsAcrossWordBoundaries`; dropping the
backslash from the `plain` table failed `TestAppendStringExact`; letting the
window run past `len(s)` failed the allocation budget (a panic is an allocation).

## Do NOT

- Migrate `consensus/wire`, `session/encode.go`, the Prometheus exporter or the
  logger's TEXT encoder: they are not JSON, and for consensus a changed byte
  forks the nodes.
- Add HTML escaping, or a mode switch for it. A caller that needs it wraps the
  result.
- Call `readOnly` on an empty string, or let the view outlive the call.
- Walk a search mask bit by bit, or search past `len(s)`.
- Put a logger, token or codec word in a signature.
