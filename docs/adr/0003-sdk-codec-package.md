# ADR 0003 — SDK `codec` Package (Universal Encode/Decode Surface)

**Status**: Accepted (M1+M2+M3+M4 shipped)
**Date**: 2026-04-19
**Deciders**: @kodflow
**Amended by**: [ADR 0156](0156-the-public-module-links-the-standard-library-and-nothing-else.md) — §M2–§M4: YAML, TOML, CBOR and MessagePack are written natively; the Format `yaml` is a named subset and the full grammar is the opt-in `yaml-full`
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
| Service | `internal/service/codec/<format>/` | Concrete codec per format. Each package registers itself on import via `var Codec codec.Codec = codec.Register(&fooCodec{})` — named singleton, no `init()` (KTN-FUNC-NOINIT). `New()` returns `Codec`. |
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
- `Register(c Codec) Codec` returns the registered codec so callers can write `var Codec codec.Codec = codec.Register(&fooCodec{})` — the named singleton binding is idiomatic Go and keeps M1 compliant with `KTN-FUNC-NOINIT` (Go 1.26 discourages `init`).
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

## Amendment — 2026-11-03: one vocabulary for bound refusals (#894)

A codec's refusal of input past one of its bounds keeps the codec's own code
— a `<X>SizeExceeded` or `<X>DepthExceeded` in its `PP` range, new for yaml,
toml, msgpack, form and cbor — and takes the family's reason: `SIZE_EXCEEDED`
for a byte, node, pair or entry bound, `DEPTH_EXCEEDED` for nesting. The
codec's `UNMARSHAL_FAILED` code joins the trail, so a caller matching any
decoding failure by code is unchanged. `core/data/codec.IsTooLarge` and
`IsTooDeep`, forwarded by the facade, answer the question across formats
(multipart's `LIMIT_EXCEEDED` and strictjson's `DOCUMENT_TOO_LARGE` count as
too large), which is what a server needs to answer 413. The prefixed reasons
(`BSON_SIZE_EXCEEDED`, `BSON_DEPTH_EXCEEDED`, `BASE_ENC_SIZE_EXCEEDED`,
`HCL_SIZE_EXCEEDED`, `PROTOBUF_SIZE_EXCEEDED`) are renamed; their codes are
not. A reason is part of `Error()`'s text, so this is a visible change.

## Amendment — 2026-11-03: a stream decoder under the caller's ceiling (#891)

`StreamingCodec.NewDecoder` takes no bound, and the json, xml and cbor
decoders held none of their own, so a socket behind `NewDecoder(JSON, conn)`
could grow the process by the size of one hostile value. A sibling port,
`BoundedStreamingCodec`, adds `NewBoundedDecoder(r, maxBytes) (Decoder,
error)` beside `StreamingCodec` rather than widening `Decoder` (ADR 0039).
The ceiling bounds one value, counted from the end of the previous one, so a
long-lived stream of small values is never refused; a value past it is
refused with the codec's `SIZE_EXCEEDED` — json `0.3.2.3`, xml `0.3.3.3`,
cbor `0.3.6.6` — after at most `maxBytes+1` bytes of it are read (cbor, whose
validator knows where an item ends, reads at most `maxBytes`). A ceiling of
zero or below is refused with `BOUND_INVALID` (0.2.2.7): a bound has no value
meaning unlimited (ADR 0031). The facade's `NewBoundedDecoder(f, r, maxBytes)`
answers `STREAMING_UNSUPPORTED` for a codec without the port; toml, msgpack
and yaml keep the fixed ceilings their `NewDecoder` already enforced.

## Amendment — 2026-11-03: a Format from an input's first bytes (#909)

The registry resolved a Format by name, MIME type or extension only, so a file
from a listing, a queue message or a request without `Content-Type` had no
path to a codec. A sibling extension, `Sniffer { Sniff(prefix) (confident
bool) }`, lets a codec claim a head no other format's output starts with —
json, ndjson (two records), xml, pem and bson; the formats almost any bytes
start (cbor, msgpack, asn1, tlv) and the text formats without a head do not
sniff. `core/data/codec.Detect` reads at most 512 bytes, as
`http.DetectContentType` does, asks every sniffer in registration order and
answers only when exactly one claims: a guess is never returned. It allocates
nothing. The facade forwards it, refusing first its own compressed frame,
whose payload a sniffer could misread, and adds `DetectCompression` for gzip,
zlib, zstd and s2 by their magic numbers — compression is a transform, never
a Format (ADR 0014).

## Amendment — 2026-11-03: one position in every decode refusal (#1125)

A decode refusal that knows where it is places itself the same way in every
format: the fields `offset` (0-based byte), `line` and `column` (1-based),
each present only when known, written through `core/data/codec.PositionFields`
or under its three keys (`FieldOffset`, `FieldLine`, `FieldColumn`), and read
back by `PositionOf` — forwarded by the facade with its value type, `Position`.
yaml and toml place by line and column (characters), csv by the line and byte
column `encoding/csv` gives — a cell its column cannot hold included —, json by
the offset `encoding/json` gives, xml by the line `encoding/xml` gives; cbor,
msgpack, bson, tlv and flatbuffers by offset alone, where an offset of zero is
the first byte, not an unknown one. bson's offset left its Private sentence,
where no reader found it, for the field; no Private text is a position's only
carrier any more. Codes, reasons and Public sentences do not change; csv's
`CELL_INVALID` and `HEADER_MISMATCH` carry the column's name under `name`, not
`column`, which is now a coordinate. The bounded decoders of #891 place their
`SIZE_EXCEEDED` at the first byte past the ceiling: json and xml at the
stream's offset, the ceiling counted from the end of the previous value, cbor
within its item, as its other stream refusals are placed.

## Amendment — 2026-11-03: misuse has the family's codes (#1101)

The three family codes the registry declared and nothing emitted now have
emitters. `core/data/codec.CheckTarget` opens every codec's `Unmarshal` and
`Decode` — the bounded decoders' of #891 included, before their window opens: a nil or non-pointer target is `TARGET_INVALID` (0.2.2.3), its type
in the `target` field — bson also takes a non-nil map, flatbuffers a
`BytesAcceptor` held by value. json, xml, bson, cbor, msgpack and tlv refuse
zero bytes with `EMPTY_INPUT` (0.2.2.2); yaml and toml read an empty document.
The codecs that encode any Go value open `Marshal` with `CheckValue`: a
channel, a function or an unsafe pointer is `VALUE_INVALID` (0.2.2.4);
`Append`, the hot path, leaves it to the encoder. The family code is the origin and no per-format code joins the
trail — misuse is not a decoding failure, and a server mapping
`UNMARSHAL_FAILED` to 400 no longer blames the client for its own bug.
`IsMisuse`, forwarded by the facade, reads the three. A per-format
`VALUE_INVALID` keeps refusing a pointer of the wrong shape, which the facade
promotes. This is a visible change: those calls returned each format's
`UNMARSHAL_FAILED`, `VALUE_INVALID`, `FLATBUFFERS_BAD_TARGET` or
`MARSHAL_FAILED`.

## References

- Plan: `/.claude/contexts/sdk-universal-codec.md`
- Plan file: `~/.claude/plans/hidden-discovering-mist.md`
- ADR 0001 — multi-module layout
- ADR 0002 — `errs` package + code registry
- ktn-linter config exception (`.ktn-linter.yaml`) — `KTN-INTERFACE-ANYUSE` scoped to codec tree
