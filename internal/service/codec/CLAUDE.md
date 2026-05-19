<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/codec/

## Purpose

Thirteen wire-format codecs covering 18 registered Format names, one Go package each, all implementing `internal/core/codec.Codec`. Blank-importing a package registers its singleton(s) with the core registry so it becomes resolvable by Name / MIME type / file extension. `pkg/v1/codec` blank-imports all thirteen in one shot; downstream applications can import only the formats they need. The `baseenc` package registers 6 distinct Format names (base64, base64url, base32, base16, hex, ascii85) under a single Go package so swapping base-N variants is a Format-string change, identical to swapping json↔cbor.

## Contents

| Package | Format(s) | `PP` slot | Streaming | Appender |
|---|---|---|---|---|
| `asn1/`        | ASN.1 DER (BER accepted on Unmarshal)         | `0.3.9.*`  | no  | no  |
| `baseenc/`     | base64, base64url, base32, base16, hex, ascii85 — 6 Format names, JSON-mediated pipeline | `0.3.24.*` | yes | yes |
| `cbor/`        | CBOR (RFC 8949) — fxamacker/cbor/v2           | `0.3.6.*`  | yes | no  |
| `csv/`         | CSV (RFC 4180) — `[][]string`                 | `0.3.8.*`  | no  | no  |
| `flatbuffers/` | FlatBuffers passthrough (already-encoded `[]byte`) | `0.3.23.*` | no  | yes |
| `json/`        | JSON (RFC 8259) — encoding/json               | `0.3.2.*`  | yes | yes |
| `msgpack/`     | MessagePack — vmihailenco/msgpack/v5          | `0.3.7.*`  | yes | no  |
| `ndjson/`      | Newline-delimited JSON                        | `0.3.11.*` | no  | yes |
| `pem/`         | PEM block (RFC 7468) — encoding/pem           | `0.3.10.*` | no  | no  |
| `tlv/`         | Self-describing TLV (reflection-driven binary)| `0.3.22.*` | yes | yes |
| `toml/`        | TOML — pelletier/go-toml/v2                   | `0.3.5.*`  | yes | no  |
| `xml/`         | XML — encoding/xml                            | `0.3.3.*`  | yes | no  |
| `yaml/`        | YAML — gopkg.in/yaml.v3                       | `0.3.4.*`  | yes | no  |

The `PP` slots above are authoritative — verified against each `codes.go`. New codecs claim a fresh slot in ADR 0005's registry (or its ADR 0006 extension) before being added.

## File layout (per codec)

```
<codec>/
├── codec.go                      # New() + Codec interface methods + Codec singleton
├── decoder.go, encoder.go        # streaming helpers (only for StreamingCodec implementers)
├── codes.go                      # const CodeXxx errs.Code  (PP slot)
├── errors.go                     # var XxxSentinel = errs.Define(...)
├── codec_internal_test.go        # white-box (per-codec quirks)
├── codec_external_test.go        # black-box (interface contract)
├── encoder_internal_test.go      # only when streaming
├── decoder_internal_test.go      # only when streaming
└── BUILD.bazel                   # gazelle-managed
```

## Conventions

- **Registration without `init()`**: each codec exposes `var Codec codec.Codec = codec.Register(&xxxCodec{})`. Initialiser order is deterministic and the AST audit forbids `init()` in this tree.
- **Stateless singletons**: `New()` returns the same `Codec` singleton, except when a codec exposes an opt-in mode (`csv.NewWithEscape(bool)` returns a fresh instance not added to the registry).
- **Defensive copies on `MIMETypes()` / `Extensions()`**: every implementer returns `slices.Clone(table)` so callers cannot mutate the package-level slice.
- **Hardening lives in the codec**: byte caps (`maxYAMLBytes`, `maxMsgPackBytes`, `scannerMaxCapacity`), structural caps (`maxCBORArrayElements`, `maxCBORMapPairs`, `maxCBORNestedLevels`), and OWASP CSV-Injection mitigation (`csv.NewWithEscape`) are codec-local — never lifted into `core/codec`.
- **Errors use the package's dotted-quad code**: every wrapped failure carries the `CodeXxxMarshalFailed` / `CodeXxxUnmarshalFailed` / `CodeXxxValueInvalid` constant from `codes.go`. Wrapping an `*errs.Error` cause is a no-op for params (origin wins).
- **Optional extensions are opt-in by interface assertion**: callers use `if a, ok := c.(codec.Appender); ok { … }` — the public registry does not promise any extension.

## Do NOT

- Add an `init()` function to register a codec — use the package-level `var Codec = codec.Register(...)` pattern.
- Import a sibling codec package. Codecs are independent; cross-codec composition belongs in `pkg/v1/codec` or in the caller.
- Use `fmt.Errorf` or bare `errors.New` to surface a third-party encode/decode failure. Wrap it through `errs.Wrap` so the dotted-quad code and reason survive.
- Mutate the singleton's MIME / extension slices — callers receive `slices.Clone` copies for a reason.
- Reach into a streaming Encoder/Decoder's `inner` field from outside the codec package.

## Subtree

- `asn1/`        — see `asn1/CLAUDE.md`
- `baseenc/`     — see `baseenc/CLAUDE.md`
- `cbor/`        — see `cbor/CLAUDE.md`
- `csv/`         — see `csv/CLAUDE.md`
- `flatbuffers/` — see `flatbuffers/CLAUDE.md`
- `json/`        — see `json/CLAUDE.md`
- `msgpack/`     — see `msgpack/CLAUDE.md`
- `ndjson/`      — see `ndjson/CLAUDE.md`
- `pem/`         — see `pem/CLAUDE.md`
- `tlv/`         — see `tlv/CLAUDE.md`
- `toml/`        — see `toml/CLAUDE.md`
- `xml/`         — see `xml/CLAUDE.md`
- `yaml/`        — see `yaml/CLAUDE.md`

## Verification

```
bazel test --config=race //internal/service/codec/...
```

ADR 0003 (universal codec package) is the authoritative design document; the AST audit in `internal/kernel/errs/registry_external_test.go` enforces uniqueness of every `Code` constant across the SDK.
