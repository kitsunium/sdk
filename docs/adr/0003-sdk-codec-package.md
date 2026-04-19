# ADR 0003 — SDK `codec` Package (Universal Encode/Decode Surface)

**Status**: Accepted (M1+M2+M3+M4 shipped)
**Date**: 2026-04-19
**Deciders**: @kodflow
**Supersedes**: none
**Related**: ADR 0001 (multi-module layout), ADR 0002 (`errs` package + code registry), plan `sdk-universal-codec`

## Context

Until this MR the SDK shipped one domain tool (logger). The next universal tool consumers asked for is a **format-agnostic encode/decode package** covering:

- Human-readable text: JSON, NDJSON, XML, YAML, TOML, CSV, HCL
- Schemaless binary: CBOR, MessagePack, BSON
- Byte encodings: Base64 (std + URL), Base32 (std + hex), Hex, Ascii85, PEM
- Schema-required binary (deferred): Protobuf, FlatBuffers, Cap'n Proto, Avro, Parquet, Arrow
- Standardised binary object format: ASN.1 DER (stdlib `encoding/asn1`)

Consumer expectations:

- one facade, many codecs, one error model;
- streaming-first where the wire format supports it;
- round-trip-safe for tagged Go structs;
- fits the SDK's 4-layer architecture (kernel → core → service → pkg/v1);
- every error goes through `errs.Define` / `errs.Wrap` (no raw `fmt.Errorf`).

## Decision

### Layered placement

| Layer | Path | Role |
|---|---|---|
| Core | `internal/core/codec/` | `Codec` / `StreamingCodec` / `Encoder` / `Decoder` interfaces, `Format` type, process-wide registry |
| Service | `internal/service/codec/<format>/` | Concrete codec per format. Each package registers itself on import via a package-level `var _ = codec.Register(&fooCodec{})` initialiser — no `init()` (KTN-FUNC-NOINIT) |
| Public | `pkg/v1/codec/` | Facade: Format constants, `Marshal`/`Unmarshal`/`NewEncoder`/`NewDecoder` dispatchers, `FromMIME`/`FromExtension`/`Available`. Blank-imports every M1 codec so a single import enables the full stdlib surface |
| Public | `pkg/v1/codec/baseenc/` | Separate sub-API for byte-only encodings — not Codec-shaped |

### Codec contract (`core/codec.Codec`)

```go
type Codec interface {
	Name() (name string)
	MIMETypes() (mimes []string)
	Extensions() (exts []string)
	Marshal(v any) (data []byte, err error)
	Unmarshal(data []byte, v any) (err error)
}

type StreamingCodec interface {
	Codec
	NewEncoder(w io.Writer) (enc Encoder)
	NewDecoder(r io.Reader) (dec Decoder)
}

type Encoder interface { Encode(v any) (err error); Close() (err error) }
type Decoder interface { Decode(v any) (err error); More() (ok bool) }
```

- Instances MUST be safe for concurrent use.
- Format-specific options are supplied via the codec's constructor, not via the interface.
- Streaming is optional — consumers detect support via a type assertion; the facade surfaces `STREAMING_UNSUPPORTED` when the assertion fails.

### Format registry

- Package-level `sync.Map`s in `internal/core/codec/registry.go` keyed by `Format` → `Codec`, plus MIME + extension indexes (lowercased).
- `Register(c Codec) (ok bool)` returns true so callers can write `var _ = codec.Register(&fooCodec{})` — this keeps M1 compliant with `KTN-FUNC-NOINIT` (Go 1.26 discourages `init`).
- `Lookup` / `LookupMIME` / `LookupExt` / `Available` expose read access. `Register` panics on nil or duplicate `Name()`.

### M1 scope

| Package | Format | Streaming | Coverage |
|---|---|---|---|
| `internal/core/codec` | interfaces + registry | n/a | core unit tests |
| `internal/service/codec/json` | JSON (stdlib `encoding/json`) | yes | 83.3% |
| `internal/service/codec/ndjson` | Newline-delimited JSON | no (slice-shaped) | 92.0% |
| `internal/service/codec/xml` | XML (stdlib `encoding/xml`) | yes | 93.8% |
| `internal/service/codec/csv` | CSV (stdlib `encoding/csv`, `[][]string`) | no | 96.2% |
| `internal/service/codec/asn1` | ASN.1 DER (stdlib `encoding/asn1`) | no | 100% |
| `internal/service/codec/pem` | PEM block (stdlib `encoding/pem`, `*pem.Block`) | no | 94.7% |
| `pkg/v1/codec` | facade | n/a | 100% |
| `pkg/v1/codec/baseenc` | Base64 std/URL, Base32 std/hex, Hex, Ascii85 | n/a | 87.9% |

### Later milestones (non-binding)

- **M2** ✅ SHIPPED — YAML (`gopkg.in/yaml.v3`). Streaming supported. Coverage 90.0%.
- **M3** ✅ SHIPPED — TOML (`github.com/pelletier/go-toml/v2`). Streaming supported. Coverage 86.7%.
- **M4** ✅ SHIPPED — CBOR (`github.com/fxamacker/cbor/v2`) + MessagePack (`github.com/vmihailenco/msgpack/v5`). Both implement StreamingCodec. Coverage 96.3% each. Module-split decision: **single `internal/service` module retained** — total transitive deps remain well below the 30-dep soft ceiling.
- **M5** (deferred, optional) — HCL, BSON, `baseenc.Base58/62/45`.
- **M6** (deferred) — Schema-based codecs (Protobuf, FlatBuffers, Cap'n Proto, Avro, Parquet, Arrow). Likely a sibling `pkg/v1/schema/` family — separate ADR per addition.

### Error codes

Reserved subranges. See ADR 0002 §Registry for the full table. Summary:

| Range | Package |
|---|---|
| 2200-2299 | `internal/core/codec` |
| 3200-3299 | `internal/service/codec/<format>` (per-format subranges 3210-3319) |
| 4200-4299 | `pkg/v1/codec` + `pkg/v1/codec/baseenc` |

Every Go error emitted from a codec is an `*errs.Error` with a stable `Reason` (`MARSHAL_FAILED`, `UNMARSHAL_FAILED`, `VALUE_INVALID`, `UNKNOWN_FORMAT`, `STREAMING_UNSUPPORTED`, `INVALID_ENCODING`, `DECODE_FAILED`). Stdlib causes are preserved through `errors.Is`.

## Consequences

### Positive

- Consumers import exactly one package to access any codec: `pkg/v1/codec`.
- Blank-import pattern keeps tree-shaking clean — a consumer who only wants JSON can copy the facade and trim.
- Strict layering holds: `pkg/v1/codec` depends only on `internal/core/codec` + `internal/kernel/errs` + the service sub-imports. No leakage in either direction.
- Uniform error surface. `errs.HasReason(err, "UNKNOWN_FORMAT")` works against any dispatch failure.
- Existing `errs` registry audit gains six more packages with zero rule-engine changes.

### Negative

- `KTN-INTERFACE-ANYUSE` had to be excluded for the codec tree via `.ktn-linter.yaml`. `Marshal(v any)` is a codec-shaped signature taken from `encoding/json`, so the exception is scoped to `internal/core/codec/**` + `internal/service/codec/**` + `pkg/v1/codec/**` and justified in the config.
- `KTN-FUNC-NOINIT` stays active everywhere (Go 1.26 guidance). Registration therefore uses a package-level `var _ = Register(...)` instead of `init()`.

### Neutral

- Streaming support is optional on the interface. Callers must be ready for `STREAMING_UNSUPPORTED`; the facade's typed sentinel makes this explicit.
- ASN.1 DER and PEM operate on their canonical wire types (`struct` for DER, `*pem.Block` for PEM) rather than arbitrary `any` — mismatches surface as `VALUE_INVALID`.

## Why not …

**… a single interface method `Marshal(v any)` without streaming?** Streaming is mandatory for large payloads (logs, event streams, NDJSON). Making it optional keeps simple codecs (CSV, ASN.1) small while letting JSON / XML stream natively.

**… auto-discovery via reflection over the service tree?** The registry is `sync.Map`-based + var-initialiser-driven. Reflection would add hidden cost and defeat tree-shaking. Explicit blank-imports at the facade are clearer.

**… putting `baseenc` inside the Codec registry?** Byte encodings transform raw bytes, not structured values. Shoe-horning them into `Codec` would force `Marshal([]byte)` / `Unmarshal([]byte, *[]byte)` contortions. Keeping them as a sibling sub-API (`pkg/v1/codec/baseenc`) is cleaner.

**… one Go module per codec?** Only warranted when the dep closure justifies it. M1 codecs are stdlib-only; the `internal/service` module absorbs them without growing go.sum. The first MR with two external deps (M4) re-evaluates.

## Deferred

- Columnar codecs (Parquet, Arrow) — defer to M6+ with their own ADR.
- Schema-based codecs (Protobuf / FlatBuffers / Cap'n Proto / Avro) — M6 or later, each its own ADR because of code-generation lifecycle.
- Compression layer (`pkg/v1/compress`) — orthogonal, future ADR.
- Primitive binary packing (`encoding/binary` BE/LE framing) — belongs in a future `pkg/v1/binpack`, not here.

## References

- Plan: `/.claude/contexts/sdk-universal-codec.md`
- Plan file: `~/.claude/plans/hidden-discovering-mist.md`
- ADR 0001 — multi-module layout
- ADR 0002 — `errs` package + code registry
- ktn-linter config exception (`.ktn-linter.yaml`) — `KTN-INTERFACE-ANYUSE` scoped to codec tree
