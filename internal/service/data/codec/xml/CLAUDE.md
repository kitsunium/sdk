<!-- updated: 2026-11-03T22:00:00Z -->
# internal/service/data/codec/xml/

## Purpose

XML codec wrapping stdlib `encoding/xml`. Streaming Encoder/Decoder are forwarded.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"xml"` |
| `MIMETypes()`    | `application/xml`, `text/xml` |
| `Extensions()`   | `.xml` |
| Constructor      | `New() codec.Codec` |
| Streaming        | yes (`NewEncoder`, `NewDecoder`); bounded (`NewBoundedDecoder(r, maxBytes)`, `core/data/codec.BoundedStreamingCodec`, #891) |
| Appender         | yes (`Append(dst, v) ([]byte, error)`) — `encoding/xml` writes straight onto `dst` through a pooled `scratch.AppendWriter` (#1124); `dst` returned as given on failure |

## Error codes (range `0.3.3.*`)

Declared in `internal/core/data/codec/xml` — `codes.go` and `errors.go` — and used
here as `corexml.<Var>` (ADR 0160 §2: a code lives in the core at the path that
mirrors the package emitting it). The values, reasons and texts are the ones this
package always emitted; only the declaration moved.

| Code         | Var               | Trigger |
|---|---|---|
| `0.3.3.1`    | `MarshalFailed`   | `encoding/xml.Marshal` returned an error |
| `0.3.3.2`    | `UnmarshalFailed` | `encoding/xml.Unmarshal` returned an error |
| `0.3.3.3`    | `SizeExceeded`    | `NewBoundedDecoder`'s decoder: a element longer than the ceiling — reason `SIZE_EXCEEDED`, `UNMARSHAL_FAILED` in the trail (#891, #894) |

**Position (#1125).** A decoding refusal carries the 1-based `line` `encoding/xml`'s `SyntaxError` gives (`position.go`), the one coordinate it reports: `codec.PositionOf` reads it back.

## Conventions

- **`Sniff` (`sniff.go`, #909)** implements `core/data/codec.Sniffer`, which `codec.Detect` asks about an input's first 512 bytes: an optional UTF-8 byte order mark and whitespace, then `<?xml` and whitespace, a comment, a DOCTYPE, or `<` and a name. It reads only the prefix and allocates nothing.
- **Billion-Laughs bounded** by stdlib: `encoding/xml` does not expand external entities and defines none of the entities a DOCTYPE declares — the declaration is skipped, and a reference to one of its entities is a syntax error (`invalid character entity`) — so the classic XXE / entity-expansion DoS vector is inert. Asserted by `TestUnmarshal_BillionLaughsBounded` in `codec_external_test.go`. No additional hardening is layered on top.
- Stateless singleton.
- **The bounded stream decoder** (`decoder.go`, #891): `NewBoundedDecoder(r, maxBytes)` puts a `window` reader between `r` and the stdlib decoder. Before each `Decode` the window opens at the decoder's `InputOffset` — the end of the previous element — and hands over at most `maxBytes+1` bytes past it, so `encoding/xml` never buffers more than that of a hostile element, however much the stream holds; a element that needed a read past the window, or that ends exactly `maxBytes+1` bytes in, is refused with `SizeExceeded`, latched — the stream cannot be resynchronised — and `More` reports false. The refusal carries the family's `offset` (#1125): the stream's byte `end-1`, the first past the ceiling — `offsetField` places nothing past what an int holds, a 32-bit platform's long stream. `Decode` calls `core/data/codec.CheckTarget` before the window opens, so a nil or non-pointer target is `TARGET_INVALID` and reads nothing (#1101). The one extra byte lets a top-level number of exactly `maxBytes` bytes see the byte that ends it. A stream of small elements, however long in all, is never refused. A ceiling of zero or below is refused with the core's `BOUND_INVALID` (ADR 0031). `NewDecoder`'s decoder is unchanged: it has no ceiling. `decoder_bounded_external_test.go` pins the edges: exactly `maxBytes`, `maxBytes+1`, two hundred small elements, a 1 MiB one read to at most `maxBytes+1` bytes.

## Performance (lib-bound)

`encoding/xml`'s reflect walk is internal to the stdlib and its token API
exposes no typed fast path, so the only lever here is pooling the outer
*bytes.Buffer that Marshal/Append encode into — now mutualised via
`internal/core/data/codec/scratch`. Do not re-add a local pool.

## Verification

```
bazel test --config=race //internal/service/data/codec/xml:xml_test
```

## Fuzz (#897)

`fuzz_external_test.go` — `FuzzUnmarshal`: any input decodes without a panic; a decoded document re-encodes and reads back equal. The seed corpus runs with the package's tests; `scripts/ci/codec-fuzz.sh` (nightly, `codec-fuzz.yml`) explores it.
