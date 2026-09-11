<!-- updated: 2026-09-11T00:00:00Z -->
# pkg/v1/codec/

## Purpose

Public facade for the universal codec dispatch. Consumers address a codec by `Format` (string alias) and call `Marshal` / `Unmarshal` / `NewEncoder` / `NewDecoder` — the package looks the format up in the `internal/core/codec` registry, type-asserts the streaming extension when needed, and forwards. Blank-imports the 16 service codec packages (covering 24 Format names — `baseenc` alone registers 9) so a single `import _ ".../pkg/v1/codec"` activates the full registry.

## Contents

```
codec.go      — Format alias, 24 Format constants, Marshal/Unmarshal/NewEncoder/NewDecoder,
                Available/FromMIME/FromExtension, resolveStreaming + unknownFormat helpers,
                blank imports for asn1|baseenc|bson|cbor|csv|flatbuffers|form|json|msgpack|multipart|ndjson|pem|tlv|toml|xml|yaml
compressed.go — MarshalCompressed / UnmarshalCompressed verbs + CompressAlgorithm alias
                (Gzip/Flate constants) + the self-describing compressed-frame codec
                (ADR 0014 D1); blank-imports internal/service/transform, which
                self-registers gzip+flate+zlib — only gzip and flate are framed
multipart.go  — MultipartForm / MultipartPart aliases + MultipartContentType: exactly what a
                consumer needs to build a file upload, send it, and read it back (see
                §Multipart below)
promote.go    — the JSON-bridge promotion path for codecs whose native shape cannot hold an
                arbitrary Go value (csv, form, pem, flatbuffers, tlv, ndjson): wrapForFormat
                builds the container, containerForFormat + the extract* closures read it
                back, and refuse any container not EXACTLY the shape the wrap stage wrote
codes.go      — CodeUnknownFormat / CodeCodecUnavailable / CodeStreamingUnsupported /
                CodePromoteFailed (range 1.2.0.*)
errors.go     — UnknownFormat / CodecUnavailable / StreamingUnsupported / PromoteFailed
                sentinels (errs.Define)
```

## Compressed frame (ADR 0014 D1)

`MarshalCompressed(f, a, v)` encodes `v` under Format `f`, compresses the result
with transform `a` (`Gzip` / `Flate`), and wraps both in a **frozen,
self-describing frame**:

```
[1B magic=0xC7][1B frame-ver=0x01][1B algID][2B innerFormatLen BE][innerFormat][payload]
```

`algID` is frozen per scheme (`gzip=0x01`, `flate=0x02`). The service layer also
registers a third stdlib scheme, `zlib` (the RFC 1950 envelope HTTP misnames
`deflate`), which has **no** frame algID: `MarshalCompressed(f, "zlib", v)`
returns `UnknownCompressor`, the documented behaviour for an unframed algorithm.
Allocating `0x03` is an additive change to a wire format frozen post-v1.0.0 and
is a `pkg/v1` decision not yet taken. `innerFormat` is the
codec Format string, so `UnmarshalCompressed(box, &v)` needs **no** Format or
Algorithm argument. A **decompression-bomb guard** (absolute 64 MiB frame
ceiling + an expansion-ratio bound above a 4 KiB small-payload floor) returns
`core/transform.CompressedFrameInvalid` — the guard thresholds are policy (tunable),
the frame **bytes** are frozen post-v1.0.0. Compression is a parallel transform
registry, **never** a codec `Format` (ADR 0014 §Why-not).

**Dep-light:** the transform schemes are **stdlib only** (`compress/gzip`,
`compress/flate`, `compress/zlib`), so the compression verbs add **zero** new
vendor modules beyond what the codec facade already carries. zstd and s2 ship in
`third-party/transform` (ADR 0066) and are deliberately **not** reachable from
here: the frame's `algID` table is frozen at `gzip=0x01` / `flate=0x02`, so
`MarshalCompressed(f, "zstd", v)` returns `UnknownCompressor` — the same
registered-but-not-framed position `zlib` has held since ADR 0014.
`CompressAlgorithm = transform.Algorithm`
is a type alias so consumers name a compressor without importing `internal/*`.

## Multipart

This facade documented the `Multipart` format's native shape as
`multipart.FormValue` and its header helper as `multipart.ContentType` while
both lived only under `internal/`, which a consumer cannot import — the
advertised uploads were reachable only as the JSON-mediated `_json` part.
`multipart.go` publishes exactly what building, sending and reading back an
upload needs, as type aliases (the `CompressAlgorithm` precedent), so there is
no conversion at the edge and the `Codec` interface is not widened (ADR 0037):

| Facade | Delegates to | Needed for |
|---|---|---|
| `MultipartForm` | `service/codec/multipart.FormValue` | the native shape to `Marshal`, and the `Unmarshal` target that yields every part |
| `MultipartPart` | `service/codec/multipart.PartValue` | a file part: `Name`, `FileName`, `ContentType`, `Data` |
| `MultipartContentType(body)` | `service/codec/multipart.ContentType` | the header the bytes must travel with |

A streaming client needs no further name: the `Encoder` from
`NewEncoder(Multipart, w)` has a `Boundary() string` method a structural
assertion reaches, and `mime.FormatMediaType` builds the header from it.
Deliberately **not** surfaced: `LimitsConfig` / `NewWithLimits` and the
`BoundaryCodec` / `BoundaryProvider` interfaces. None is needed to build or
send an upload, the registered codec already carries the ADR 0031 defaults,
and raising a decode-side ceiling is a public-API decision of its own; the
facade never documented them, so there was no claim to correct.
`multipart_external_test.go` imports nothing under `internal/` — it is the
proof that the three names suffice.

## Conventions

- **`Format` is the public dispatch key.** It's `type Format = corecodec.Format` — a string alias, but the 24 named constants (`JSON`, `NDJSON`, `XML`, `CSV`, `Form`, `ASN1DER`, `PEM`, `YAML`, `TOML`, `CBOR`, `MsgPack`, `TLV`, `FlatBuffers`, `Base64`, `Base64URL`, `Base32`, `Base16`, `Hex`, `ASCII85`, `Base45`, `Base58`, `Base62`, `BSON`, `Multipart`) are the contract. Their string values are frozen post-v1.0.0.
- **Lookup is `// IFACE-PLUGIN`.** `corecodec.Lookup`, `LookupMIME`, `LookupExt`, and `Available` (the implementations behind the four facade entry points) are the canonical plugin discovery surface. The 16 service codec packages register themselves via package-level `var` side-effects driven by the blank imports in `codec.go`.
- **Origin wins.** When the underlying codec returns an `*errs.Error`, this package forwards it untouched. Only dispatch-level failures (unknown format, non-streaming codec) get a new sentinel built in this package.
- **Error codes use range 1.2.0.*** per ADR 0005:
  - `1.2.0.1` `CodeUnknownFormat` — `corecodec.Lookup` returned `false`.
  - `1.2.0.2` `CodeCodecUnavailable` — registry entry exists but codec is unusable (reserved for future build-tag gating).
  - `1.2.0.3` `CodeStreamingUnsupported` — `NewEncoder` / `NewDecoder` called on a codec that doesn't implement `corecodec.StreamingCodec`.
  - `1.2.0.4` `CodePromoteFailed` — the promotion path cannot serve the request: no strategy for the Format, or a container whose shape is not exactly the one the wrap stage writes (a form body with a second key beside `_json`, a csv table with an extra row or column) — refused rather than decoded past, because the extra data would be dropped without a word.
- **The sentinel vars (`UnknownFormat`, `CodecUnavailable`, `StreamingUnsupported`) exist** for `errs.HasReason` matching, but the dispatch functions construct fresh wrapped errors with `errs.Wrap(nil, ...)` so the `Private` field can name the offending Format / codec.

## Do NOT

- Add a new `Format` constant without registering its service codec under the same name and updating `MODULE.bazel` if a new external dependency is needed.
- Re-export `corecodec.Register` here — registration is `init()`-driven by service packages; consumers do not register codecs.
- Bypass the registry from a consumer by importing a service codec package directly. Stay on `pkg/v1/codec`.
- Rename an existing `Format` string value — it's part of the frozen public contract.
- Surface `errs.PrivateOf` output from codec errors to end users; the Private field names internal package paths.

## Verification

```
bazel test --config=race //pkg/v1/codec:codec_test
# Fallback:
cd pkg/v1 && GOWORK=off go test -race ./codec/...
```

`codec_external_test.go` covers Marshal/Unmarshal round-trips for each registered format and `codec_internal_test.go` exercises the `resolveStreaming` / `unknownFormat` branches.

## Subtree

_(none — every codec lives under `internal/service/codec/*` and is reached via the universal dispatch above. The legacy byte-level `baseenc/` subpackage was removed in favour of `codec.Marshal("base64"|"base64url"|"base32"|"base16"|"hex"|"ascii85", v)`.)_
