<!-- updated: 2026-05-18T14:30:00Z -->
# pkg/v1/codec/baseenc/

## Purpose

Uniform `Encoding`-tagged wrapper over Go's byte-encoding stdlibs (`encoding/hex`, `encoding/base32`, `encoding/base64`, `encoding/ascii85`). This package is the **byte-level API** — raw bytes in, raw text out, no JSON envelope, no codec registry.

### Dual surface (since 2026-05-18)

There are now two ways to base-N encode in the SDK:

1. **This package (`pkg/v1/codec/baseenc`)** — the legacy byte-level API. `baseenc.Encode(Base64Std, raw)` / `baseenc.Decode(Base64Std, text)`. No JSON envelope. Use when you have raw bytes and need a textual alphabet.
2. **`internal/service/codec/baseenc`** — six `core/codec.Codec` implementations (`base64`, `base64url`, `base32`, `base16`, `hex`, `ascii85`) registered under the universal Marshal/Unmarshal dispatch. Pipeline is JSON-mediated: `Marshal(v)` runs `encoding/json.Marshal` first, then base-N encodes the JSON bytes. Activated automatically when `pkg/v1/codec` is blank-imported.

```go
// Byte-level (this package):
text, _ := baseenc.Encode(baseenc.Base64Std, []byte("hello"))
// → "aGVsbG8="

// Codec-shaped (universal dispatch):
import _ "github.com/kitsunium/sdk/pkg/v1/codec"
enc, _ := codec.Marshal("base64", map[string]int{"k": 1})
// → base64-encoded `{"k":1}`
```

Both surfaces coexist by design — the byte-level form is preserved for back-compat per the pkg/v1 freeze policy.

## Contents

```
baseenc.go  — Encoding (int + iota), the 7 constants + String(),
              Encode / Decode dispatch, encodeASCII85 / decodeASCII85 / wrapDecode helpers
codes.go    — CodeInvalidEncoding / CodeDecodeFailed / CodeEncodeFailed (range 1.2.1.*)
errors.go   — InvalidEncoding / DecodeFailed / EncodeFailed sentinels (errs.Define)
```

## Conventions

- **`Encoding` is a typed `int` + `iota` enum**, not a string. `EncodingUnknown` is the zero value and is reserved as the invalid sentinel; the `switch` in `Encode` / `Decode` lists it explicitly and falls through into `INVALID_ENCODING`. Out-of-range int values also produce `INVALID_ENCODING`.
- **Numeric values are stable across releases** — new encodings MUST be appended at the end of the iota block so existing const values never shift. Don't reorder.
- **Migration precedent.** Pre-PR #25 `Encoding` was a string. PR #25 (breaking, pre-1.0.0) migrated it to the typed-int form to let the compiler catch typos at call sites. Documented under the "version freeze policy" in `pkg/v1/CLAUDE.md`.
- **Encoding-side / Decoding-side error separation.** `ENCODE_FAILED` (`1.2.1.3`) only fires from `encodeASCII85` (the only encoder that can fail buffered); `DECODE_FAILED` (`1.2.1.2`) wraps every stdlib decoder failure. Consumers branch on `errs.HasReason(err, "ENCODE_FAILED" | "DECODE_FAILED" | "INVALID_ENCODING")`.
- **Error codes use range 1.2.1.*** per ADR 0005:
  - `1.2.1.1` `CodeInvalidEncoding` — `Encoding` value not in the known set.
  - `1.2.1.2` `CodeDecodeFailed` — stdlib decoder rejected the input bytes.
  - `1.2.1.3` `CodeEncodeFailed` — stdlib encoder failed mid-buffer (ascii85 only today).
- `String()` returns the canonical short name (`"base64"`, `"base64url"`, `"base32"`, `"base32hex"`, `"hex"`, `"ascii85"`); zero / out-of-range yields `"unknown"`.

## Do NOT

- Treat `baseenc` as a `core/codec.Codec` — it does not register with the codec registry; it has no `Format` constant; there is no `Marshal`/`Unmarshal` entry point. If a consumer needs byte-level base-N encoding inside a structured payload, they call `baseenc.Encode` / `Decode` themselves.
- Reorder or remove an `Encoding` constant — the numeric values are part of the frozen public contract.
- Compare `Encoding` to a string literal. Use the named constants (`baseenc.Base64Std`, etc.).
- Surface `errs.PrivateOf` output to end users; `Private` names the failing encoder/decoder.

## Verification

```
bazel test --config=race //pkg/v1/codec/baseenc:baseenc_test
# Fallback:
cd pkg/v1 && GOWORK=off go test -race ./codec/baseenc/...
```

`baseenc_external_test.go` covers round-trips for every encoding plus the `INVALID_ENCODING` / `DECODE_FAILED` paths; `baseenc_internal_test.go` exercises the ascii85 buffered-encode helper.
