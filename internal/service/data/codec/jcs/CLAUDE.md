<!-- updated: 2026-11-03T00:00:00Z -->
# internal/service/data/codec/jcs/

## Purpose

The JSON Canonicalization Scheme of RFC 8785 (#895): one byte form for a JSON document, so it can be signed or hashed and checked by anyone who canonicalizes the same document — JWK thumbprints (`crypto/key/jwk` uses it), detached JWS payloads, webhook signatures, digests over documents. Not a codec Format: a canonical form is a policy over JSON, as `strictjson` is a policy over decoding (ADR 0102 and its amendment of 2026-11-03). Public facade: `pkg/v1/data/codec/jcs`. Codes `0.3.102.*`, declared in `internal/core/data/codec/jcs`.

## Surface

| Symbol | Contract |
|---|---|
| `Canonicalize(doc []byte) ([]byte, error)` | the canonical form of one I-JSON document; `NotJSON` (offset only) or `NumberInvalid` |
| `Marshal(v any) ([]byte, error)` | json/v2's encoding of `v` under its defaults (invalid UTF-8 refused, as I-JSON requires), then `Canonicalize`; `MarshalFailed` when json/v2 refuses |

## How it is written

- **Read strictly with `encoding/json/jsontext`'s defaults**, which are I-JSON's (RFC 7493): a duplicate member name, invalid UTF-8, trailing data or a truncation is `NotJSON`, at the reader's offset. The decoder is pooled (`readerPool`): a `jsontext.Decoder` carries kilobytes of state a four-member document would otherwise allocate per call.
- **Objects** — each member's value is canonicalized into the object's own buffer, then members are written sorted by their names' UTF-16 code units (§3.2.3, `compareUTF16`): byte order, except that a character past U+FFFF (a surrogate pair) sorts before U+E000–U+FFFF.
- **Strings** (§3.2.2.2) — only `"`, `\` and the C0 controls are escaped: `\b \t \n \f \r` by name, the others `\u00xx` in lower case; `/`, `<`, U+2028 and everything else as themselves.
- **Numbers** (§3.2.2.3) — parsed to the nearest IEEE 754 double (`strconv.ParseFloat`; past the largest finite double is `NumberInvalid`, an underflow reads as 0 as ECMAScript reads it), written as ECMAScript's `Number::toString`: the shortest round-trip digits, decimal notation for 1e-6 ≤ |x| < 1e21, exponent notation outside with the sign and no leading zero (`e-7`, `e+21`), `-0` as `0`. That is encoding/json's spelling of a float64, so the digits are `kernel/jsonenc.AppendFloat`'s (#1039), the SDK's one float writer; only the negative zero is the canonicalizer's own, since encoding/json writes `-0`. RFC 8785 Appendix B's table is `TestRFC8785Numbers`, and it is the judge of any change to that writer.

## Tests

`jcs_external_test.go`: §3.2.4's example, §3.2.3's sort, Appendix B's numbers (NaN and Infinity refused by `Marshal`), the refusals — none quoting the document — and nesting as a fixed point. Fuzzing the canonicalizer is #897's (the family's fuzz targets).

## Benchmarks

`jcs_bench_test.go` — `Canonicalize8KB`, and a JWK's four members through `Marshal` and `Canonicalize`; figures in `BENCH.md`.

## Do NOT

- Register it as a Format: a canonical form has no MIME type and no reader of its own.
- Quote the document in an error: what is signed is routinely a record holding personal data.
