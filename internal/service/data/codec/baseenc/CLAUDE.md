<!-- updated: 2026-11-03T12:00:00Z -->
# internal/service/data/codec/baseenc/

## Purpose

Family of `core/data/codec.Codec` implementations behind the universal
Marshal/Unmarshal dispatch. Most wrap the stdlib byte encodings
(`encoding/base64`, `encoding/base32`, `encoding/hex`, `encoding/ascii85`);
`base45` (RFC 9285), `base58` (Bitcoin) and `base62` have no stdlib backing
and are hand-rolled (`base45.go`, `base_convert.go` + `base58.go`/`base62.go`).
Nine variants, nine distinct registered Formats — the discriminator is the
registered Name, not a tagged enum.

## Surface

| Variant | Name | MIME types | Extensions | Streaming | Appender |
|---|---|---|---|---|---|
| Base64 (std) | `"base64"` | `application/base64`, `text/base64` | `.b64`, `.base64` | yes | yes |
| Base64URL | `"base64url"` | `application/base64url` | `.b64url` | yes | yes |
| Base32 (std) | `"base32"` | `application/base32` | `.b32` | yes | yes |
| Base16 (upper) | `"base16"` | `application/base16` | `.b16` | yes (buffered) | yes |
| Hex (lower) | `"hex"` | `application/hex` | `.hex` | yes | yes |
| Ascii85 | `"ascii85"` | `application/ascii85` | `.a85` | yes | yes |
| Base45 (RFC 9285) | `"base45"` | `application/base45` | `.b45` | yes (buffered) | yes |
| Base58 (Bitcoin) | `"base58"` | `application/base58` | `.b58` | yes (buffered) | yes |
| Base62 | `"base62"` | `application/base62` | `.b62` | yes (buffered) | yes |

Nine singletons exported (`Base64`, `Base64URL`, `Base32`, `Base16`, `Hex`,
`ASCII85`, `Base45`, `Base58`, `Base62`) — registered via package-level var
initialisers, no `init()` function.

- **Base45** is a block transform (2 bytes → 3 chars), O(n), so it shares the
  10 MiB `maxBaseEncBytes` cap; decode errors reuse `BaseEncDecodeFailed`.
- **Base58 / Base62** are big-endian **base-conversion** encodings (whole input
  treated as one integer, divided down by the radix) — inherently **O(n²)**, so
  they carry a cap enforced on **every** path: `maxConvBytes` (64 KiB) on
  the raw bytes at Marshal/Append and at the streaming encode (`bufferingWriter`
  Close), and `maxConvEncodedBytes` (128 KiB — the ~1.37× raw→encoded expansion)
  on the encoded text at Unmarshal and at the streaming decode
  (`decodeAllReader`); all surface `BaseEncSizeExceeded`. They are for
  identifiers, keys, hashes and CIDs; use base64 for bulk data — they are
  excluded from the bulk streaming round-trip test for that reason. Decode
  errors reuse `BaseEncDecodeFailed`. Leading zero bytes map to leading
  `alphabet[0]` chars.
  **The arithmetic is limb-wise** (#898, `base_convert.go`): the magnitude is
  held in 32-bit limbs and divided — on encode — or multiplied — on decode — by
  58⁵ or 62⁵ per step through a uint64 accumulator, five digits at once, the
  Bitcoin Core / multiformats approach; the two shipped radices divide by a
  constant, which the compiler turns into a multiplication. Still quadratic,
  about twenty-five times fewer steps: 4 KiB encodes in 2.5 ms (was 69 ms) and
  decodes in 0.56 ms (was 22 ms) (`BENCH.md` §#898), which is what let the cap
  rise from 4 KiB to 64 KiB, as #898 set it. Per byte of input an attacker
  sends, the decode at the new cap costs less than at the old one.
  `TestLimbConversionMatchesLongDivision` and `FuzzBaseConvRoundTrip` hold the
  output byte for byte to the byte-wise long division it replaced, kept in the
  tests as `referenceEncodeBaseN` / `referenceDecodeBaseN`.
  The base-conversion **decode** allocates twice per call regardless of size:
  the limb accumulator is reserved once (radix^n < 2^(6n) for every radix below
  64) and the output once.

## Marshal/Unmarshal pipeline

The codec is **JSON-mediated**. `Marshal(v)` runs `encoding/json.Marshal`
then base-N encodes the JSON bytes; `Unmarshal(data, v)` reverses the
pipeline. Even `[]byte` values flow through JSON first — encoding/json's
`[]byte`→base64-string convention applies inside the JSON payload, then
the outer base-N step wraps the JSON.

## Error codes (range `0.3.24.*`)

Declared in `internal/core/data/codec/baseenc` — `codes.go` and `errors.go` — and used
here as `corebaseenc.<Var>` (ADR 0160 §2: a code lives in the core at the path that
mirrors the package emitting it). The values, reasons and texts are the ones this
package always emitted; only the declaration moved.

| Code         | Var                     | Trigger |
|---|---|---|
| `0.3.24.1`   | `BaseEncMarshalFailed`  | `encoding/json.Marshal` failed before the base-N step |
| `0.3.24.2`   | `BaseEncUnmarshalFailed`| `encoding/json.Unmarshal` failed after the base-N step |
| `0.3.24.3`   | `BaseEncDecodeFailed`   | stdlib base-N decoder rejected the input bytes |
| `0.3.24.4`   | `BaseEncSizeExceeded`   | `len(data)` exceeds `maxBaseEncBytes` (10 MiB) — CWE-400 |

## Conventions

- **Hard cap on Unmarshal**: 10 MiB (`maxBaseEncBytes`) — CWE-400 defence
  before the base-N decoder allocates.
- **Append uses stdlib `AppendEncode`** (Go 1.22+) for base64/base32/hex.
  Ascii85 has no `AppendEncode`, so the buffer is encoded into a sized
  scratch slice then `append`ed onto `dst`. The JSON pre-step always
  allocates; base-N Append is "encoding-step zero-alloc" only.
- **Base16 emits its own alphabet; it does not post-process `hex`.**
  `encodeHexUpperInto` indexes `hexUpperAlphabet` directly, and
  `appendEncodeBase16Upper` reserves the width with `slices.Grow` and writes
  into it. Both stdlib-based shapes that preceded it walked the output a
  second time to fix case, which cost 3.13× the stdlib lowercase encoder;
  base16 now costs what `hex` costs (`BENCH.md` §4). Do not "simplify" either
  back to `hex.Encode` + a case pass.
- **Streaming** wraps the stdlib base-N reader/writer in a json.Encoder /
  json.Decoder. Base16 (uppercase) has no streaming form in stdlib so
  the encoder buffers all writes and runs `encodeBytes` at Close.
- **Stateless singletons** — `baseencCodec.variant` is the only state;
  the package-level `variantSpecs` table drives `Name`/`MIMETypes`/
  `Extensions`. `MIMETypes` and `Extensions` return `slices.Clone` copies.
- **Append rollback**: a JSON-side failure returns `dst[:origLen]` so the
  caller's buffer is restored exactly as passed in (Appender contract).

## Do NOT

- Add a new variant without claiming a fresh slot in the `variantSpecs`
  array AND extending the `variant` iota — the spec index must equal
  the variant value.
- Bypass the JSON wrap layer in Marshal/Unmarshal — the universal "flatten
  to JSON, then base-N" semantic is intentional. For raw-byte base-N
  encoding without the JSON envelope, call stdlib `encoding/base64`,
  `encoding/base32`, `encoding/hex`, or `encoding/ascii85` directly; the
  SDK does not ship a parallel byte-level surface (the former
  `pkg/v1/data/codec/baseenc` was removed for uniformity).

## Performance

`BENCH.md` is the **choice table** for the nine variants: cost AND expansion,
side by side, at the same 1 KiB payload. Regenerate with
`cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem ./data/codec/baseenc/`.
The one-line summary a caller needs: base58/base62 cost ~2 770× base64 for
0.03 expansion points, so they are chosen for what the alphabet LOOKS like,
never for size; among the block encodings ascii85 is the most compact (1.250×)
at 2× base64's cost, and base32's decode is 3.8× its own encode.

## Verification

```
bazel test --config=race //internal/service/data/codec/baseenc:baseenc_test
# fuzz one target at a time (fuzz_internal_test.go): FuzzBaseConvRoundTrip,
# FuzzBaseConvDecode, FuzzBase45RoundTrip, FuzzBase45Decode
cd internal/service && GOWORK=off go test -run='^$' -fuzz='^FuzzBaseConvDecode$' -fuzztime=30s ./data/codec/baseenc/
```

The four fuzz targets cover the hand-rolled transforms, base conversion and
Base45: exact recovery of any byte string, its leading zeros included, and
canonicality — an input the decoder accepts re-encodes to itself. Their seed
corpora run with the ordinary suite.
