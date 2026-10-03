<!-- updated: 2026-10-03T00:00:00Z -->
# internal/service/data/codec/

## Purpose

Sixteen wire-format codecs covering 24 registered Format names, one Go package each, all implementing `internal/core/data/codec.Codec`. Blank-importing a package registers its singleton(s) with the core registry so it becomes resolvable by Name / MIME type / file extension. `pkg/v1/data/codec` blank-imports all sixteen in one shot; downstream applications can import only the formats they need. The `baseenc` package registers 9 distinct Format names (base64, base64url, base32, base16, hex, ascii85, base45, base58, base62) under a single Go package so swapping base-N variants is a Format-string change, identical to swapping json↔cbor.

## Contents

| Package | Format(s) | `PP` slot | Streaming | Appender |
|---|---|---|---|---|
| `asn1/`        | ASN.1 DER (BER accepted on Unmarshal)         | `0.3.9.*`  | no  | yes |
| `baseenc/`     | base64, base64url, base32, base16, hex, ascii85, base45, base58, base62 — 9 Format names, JSON-mediated pipeline | `0.3.24.*` | yes | yes |
| `bson/`        | BSON 1.1 (MongoDB) — native, standard library only | `0.3.36.*` | no  | yes |
| `cbor/`        | CBOR (RFC 8949) — native, standard library    | `0.3.6.*`  | yes | yes |
| `csv/`         | CSV (RFC 4180) — `[][]string`                 | `0.3.8.*`  | no  | yes |
| `flatbuffers/` | FlatBuffers passthrough (already-encoded `[]byte`) | `0.3.23.*` | no  | yes |
| `form/`        | `application/x-www-form-urlencoded` — `url.Values` | `0.3.40.*` | no  | yes |
| `json/`        | JSON (RFC 8259) — encoding/json               | `0.3.2.*`  | yes | yes |
| `msgpack/`     | MessagePack — native, standard library only   | `0.3.7.*`  | yes | yes |
| `multipart/`   | multipart/form-data (RFC 7578) — mime/multipart | `0.3.41.*` | yes | yes |
| `ndjson/`      | Newline-delimited JSON                        | `0.3.11.*` | no  | yes |
| `pem/`         | PEM block (RFC 7468) — encoding/pem           | `0.3.10.*` | no  | yes |
| `tlv/`         | Self-describing TLV (reflection-driven binary)| `0.3.22.*` | yes | yes |
| `toml/`        | TOML v1.0.0 — native, standard library only   | `0.3.5.*`  | yes | yes |
| `xml/`         | XML — encoding/xml                            | `0.3.3.*`  | yes | yes |
| `yaml/`        | YAML — a named subset of YAML 1.2.2, native (standard library only); the full reader is `third-party/codec/yaml` (`"yaml-full"`) | `0.3.4.*`  | yes | yes |

The `PP` slots above are authoritative — verified against each package's `Code*` constants. No package here declares one: since ADR 0160 every code and every `errs.Define` sentinel of `<codec>/` lives in `internal/core/data/codec/<codec>/` (`codes.go` and `errors.go`), the core path that mirrors it, and the package imports it as `core<codec>`. The values did not change when the declarations moved — `LL = 3` records the layer that allocated the range, not the directory that declares it. A check refuses `errs.Define` under `internal/service`. New codecs claim a fresh slot in ADR 0005's registry (or its ADR 0006 extension), in a new core mirror, before being added.

`jsonshape/` is not a codec either (ADR 0133): it encodes nothing, and describes
how values of a Go type look under `encoding/json` — the members an object has,
resolved by the Go 1.27 engine's own rules, which may be missing or null, and the
Go field behind each. It declares no codes and is reached through
`pkg/v1/data/codec/jsonshape`.

`jsonpatch/` is not a codec either (ADR 0143 §D9): it encodes nothing, and
computes the structural difference between two JSON documents as RFC 6902
operations — add, remove, replace at a JSON Pointer — with the value each
writes and the value it replaces. Documents are read strictly with
`encoding/json/jsontext` and compared as RFC 6902 §4.6 compares values, numbers
exactly; arrays are aligned before they are paired. It owns `0.3.90.*` and is
reached through `pkg/v1/data/codec/jsonpatch`.

Four of the codecs above are also reachable ONE AT A TIME: `pkg/v1/data/codec/json`,
`pkg/v1/data/codec/yaml`, `pkg/v1/data/codec/toml` and `pkg/v1/data/codec/bson` each import
their own package here and nothing else (ADR 0134), so a program reading YAML
configuration links the native YAML reader — no third-party library at all — and
no other codec. `pkg/v1/data/codec/bson`
also aliases BSON's value types, which `bson/` owns. A codec package registers
itself in its own initialisation, which Go runs once, so being imported by both
a per-format facade and `pkg/v1/data/codec` registers it once.

`strictjson/` sits in this tree and is deliberately NOT a codec (ADR 0102): it
registers no Format and implements no `core/data/codec.Codec`, because what it adds —
a per-call byte bound, the refusal of unknown and case-variant members, of
duplicate names and of trailing data, and errors that never quote the input —
is a decoding POLICY for documents somebody else wrote, not a wire format. It is
built on `encoding/json/v2`, owns `0.3.72.*`, and is reached through its own
facade `pkg/v1/data/codec/strictjson`, so a server that only needs it does not link
the sixteen codecs `pkg/v1/data/codec` blank-imports. `json/` is unchanged.

## File layout (per codec)

```
<codec>/
├── codec.go                      # New() + Codec interface methods + Codec singleton
├── decoder.go, encoder.go        # streaming helpers (only for StreamingCodec implementers)
├── errors.go                     # failure constructors (errs.Wrap with the core's codes), where the codec has any
│                                 # (yaml/ is a parser and an encoder of its own, laid out by concern — see yaml/CLAUDE.md)
├── codec_internal_test.go        # white-box (per-codec quirks)
├── codec_external_test.go        # black-box (interface contract)
├── codec_integration_test.go     # //go:build !race AllocsPerRun budgets, run by the race-off alloc lane (all but multipart)
├── encoder_internal_test.go      # only when streaming
├── decoder_internal_test.go      # only when streaming
└── BUILD.bazel                   # gazelle-managed
```

## Conventions

- **Registration without `init()`**: each codec exposes `var Codec codec.Codec = codec.Register(&xxxCodec{})`. Initialiser order is deterministic, and ktn-linter's `KTN-FUNC-NOINIT` reports any `init()` — active everywhere, with no exclusion in `.ktn-linter.yaml`.
- **Stateless singletons**: `New()` returns the same `Codec` singleton; the two opt-in modes — `csv.NewWithEscape(bool)` and `multipart.NewWithLimits(LimitsConfig)` — each return a fresh instance not added to the registry.
- **Defensive copies on `MIMETypes()` / `Extensions()`**: every implementer returns a slice the caller owns — `slices.Clone(table)`, or a fresh literal in `asn1`, `csv` and `json` — so callers cannot mutate a package-level slice.
- **Hardening lives in the codec**: byte caps (`maxYAMLBytes`, `maxMsgPackBytes`, `maxBSONBytes`, `scannerMaxCapacity`, `form.maxFormBytes`, `toml.maxDocumentBytes`), structural caps (`maxCBORArrayElements`, `maxCBORMapPairs`, `maxCBORNestedLevels`, `maxCBORStringChunks`, `maxBSONNestedLevels`, `form.maxFormPairs`, `msgpack.maxDepth`, `toml.maxDepth`, YAML's `maxDepth`, `maxNodes` and `maxKeyRunes`), and OWASP CSV-Injection mitigation (`csv.NewWithEscape`) are codec-local — never lifted into `core/data/codec`. They are `const`, not constructor options — except `multipart`'s three bounds, which `NewWithLimits` takes with a zero field meaning the package default and a negative one refused: a tunable bound whose zero value silently means "unlimited" is exactly what ADR 0031 forbids.
- **Errors use the package's dotted-quad code**: every wrapped failure carries the `CodeXxxMarshalFailed` / `CodeXxxUnmarshalFailed` / `CodeXxxValueInvalid` constant its core mirror declares (`internal/core/data/codec/<codec>/codes.go`, ADR 0160). Wrapping an `*errs.Error` cause is a no-op for params (origin wins).
- **Optional extensions are opt-in by interface assertion**: callers use `if a, ok := c.(codec.Appender); ok { … }` — the public registry does not promise any extension.

## Do NOT

- Add an `init()` function to register a codec — use the package-level `var Codec = codec.Register(...)` pattern.
- Import a sibling codec package. Codecs are independent; cross-codec composition belongs in `pkg/v1/data/codec` or in the caller.
- Use `fmt.Errorf` or bare `errors.New` to surface a third-party encode/decode failure. Wrap it through `errs.Wrap` so the dotted-quad code and reason survive.
- Mutate the singleton's MIME / extension slices — callers receive their own copies for a reason.
- Reach into a streaming Encoder/Decoder's `inner` field from outside the codec package.

## Subtree

- `asn1/`        — see `asn1/CLAUDE.md`
- `baseenc/`     — see `baseenc/CLAUDE.md`
- `bson/`        — see `bson/CLAUDE.md`
- `cbor/`        — see `cbor/CLAUDE.md`
- `csv/`         — see `csv/CLAUDE.md`
- `flatbuffers/` — see `flatbuffers/CLAUDE.md`
- `form/`        — see `form/CLAUDE.md`
- `json/`        — see `json/CLAUDE.md`
- `jsonpatch/`   — see `jsonpatch/CLAUDE.md` (not a codec — ADR 0143)
- `jsonshape/`   — see `jsonshape/CLAUDE.md` (not a codec — ADR 0133)
- `msgpack/`     — see `msgpack/CLAUDE.md`
- `multipart/`   — see `multipart/CLAUDE.md`
- `ndjson/`      — see `ndjson/CLAUDE.md`
- `pem/`         — see `pem/CLAUDE.md`
- `strictjson/`  — see `strictjson/CLAUDE.md` (not a codec — ADR 0102)
- `tlv/`         — see `tlv/CLAUDE.md`
- `toml/`        — see `toml/CLAUDE.md`
- `xml/`         — see `xml/CLAUDE.md`
- `yaml/`        — see `yaml/CLAUDE.md`

## Verification

```
bazel test --config=race //internal/service/data/codec/...
```

ADR 0003 (universal codec package) is the authoritative design document; the AST audit in `internal/kernel/errs/registry_external_test.go` enforces uniqueness of every `Code` constant across the SDK.
