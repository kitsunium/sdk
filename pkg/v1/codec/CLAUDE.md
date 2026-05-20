<!-- updated: 2026-05-18T14:30:00Z -->
# pkg/v1/codec/

## Purpose

Public facade for the universal codec dispatch. Consumers address a codec by `Format` (string alias) and call `Marshal` / `Unmarshal` / `NewEncoder` / `NewDecoder` — the package looks the format up in the `internal/core/codec` registry, type-asserts the streaming extension when needed, and forwards. Blank-imports the 10 service codec packages so a single `import _ ".../pkg/v1/codec"` activates the full registry.

## Contents

```
codec.go    — Format alias, 10 Format constants, Marshal/Unmarshal/NewEncoder/NewDecoder,
              Available/FromMIME/FromExtension, resolveStreaming + unknownFormat helpers,
              blank imports for asn1|cbor|csv|json|msgpack|ndjson|pem|toml|xml|yaml
codes.go    — CodeUnknownFormat / CodeCodecUnavailable / CodeStreamingUnsupported (range 1.2.0.*)
errors.go   — UnknownFormat / CodecUnavailable / StreamingUnsupported sentinels (errs.Define)
```

## Conventions

- **`Format` is the public dispatch key.** It's `type Format = corecodec.Format` — a string alias, but the 10 named constants (`JSON`, `NDJSON`, `XML`, `CSV`, `ASN1DER`, `PEM`, `YAML`, `TOML`, `CBOR`, `MsgPack`) are the contract. Their string values are frozen post-v1.0.0.
- **Lookup is `// IFACE-PLUGIN`.** `corecodec.Lookup`, `LookupMIME`, `LookupExt`, and `Available` (the implementations behind the four facade entry points) are the canonical plugin discovery surface. The 10 service codec packages register themselves via `init()` side-effects driven by the blank imports in `codec.go`.
- **Origin wins.** When the underlying codec returns an `*errs.Error`, this package forwards it untouched. Only dispatch-level failures (unknown format, non-streaming codec) get a new sentinel built in this package.
- **Error codes use range 1.2.0.*** per ADR 0005:
  - `1.2.0.1` `CodeUnknownFormat` — `corecodec.Lookup` returned `false`.
  - `1.2.0.2` `CodeCodecUnavailable` — registry entry exists but codec is unusable (reserved for future build-tag gating).
  - `1.2.0.3` `CodeStreamingUnsupported` — `NewEncoder` / `NewDecoder` called on a codec that doesn't implement `corecodec.StreamingCodec`.
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
