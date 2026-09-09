# internal/service/codec/tlv/

## Purpose

Self-describing Type-Length-Value codec. Reflection-driven encoder writes
arbitrary Go values; decoder reconstructs them as Go-native types
(`int64`, `uint64`, `float64`, `string`, `[]byte`, `[]any`, `map[any]any`,
`map[string]any`). Wire layout is `tag(1B) length(varint) value(...)`.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"tlv"` |
| `MIMETypes()`    | `application/x-tlv`, `application/vnd.tlv` |
| `Extensions()`   | `.tlv` |
| Constructor      | `New() codec.Codec` |
| Streaming        | yes (`NewEncoder`, `NewDecoder` — one record per Encode/Decode) |
| Appender         | yes (`Append(dst, v) ([]byte, error)`) — drops Marshal's fresh-slice alloc; benches at 1 alloc/op, the best of the 22 formats benchmarked in `pkg/v1/codec/BENCH.md` |

## Error codes (range `0.3.22.*`)

| Code         | Var                | Trigger |
|---|---|---|
| `0.3.22.1`   | `MarshalFailed`    | encode-side failure (writer error, oversize field name) |
| `0.3.22.2`   | `UnmarshalFailed`  | malformed record, type mismatch, decoder shape error |
| `0.3.22.3`   | `UnsupportedType`  | chan / func / complex* / unsafe.Pointer source |
| `0.3.22.4`   | `DepthExceeded`    | nesting depth > `maxTLVDepth` (32) — CWE-674 |
| `0.3.22.5`   | `SizeExceeded`     | Unmarshal input > `maxTLVBytes` (10 MiB) — CWE-400 |
| `0.3.22.6`   | `Truncated`        | buffer ends mid-record |

## Conventions

- **Reflection-driven**, self-describing format. Tags are 1-byte
  discriminators grouped by family (`0x01` nil, `0x02-03` bool,
  `0x10-13` int*, `0x20-23` uint*, `0x30-31` float*, `0x40-41`
  string/bytes, `0x50` slice, `0x60` map, `0x70` struct).
- **Narrowest tag wins** on encode: an `int(42)` is emitted as `tagInt8`,
  not `tagInt64`. Decoding always upgrades to the widest Go type
  (`int64` / `uint64` / `float64`) and re-narrows via `reflect.Convert`
  when assigning back to a typed pointer.
- **Hardening caps**: input buffer ≤ 10 MiB (`maxTLVBytes`), nesting
  depth ≤ 32 (`maxTLVDepth`), struct field-name ≤ 255 bytes
  (`maxFieldNameBytes`), pre-allocation hint clamped to 4096
  (`sliceHintCap`). Mirrors the msgpack / NDJSON cap conventions.
- **`reflectView` wrapper**: a local named type (`type reflectView
  reflect.Value`) that lets package-internal helpers accept reflect
  values without the external concrete type tripping the linter's
  consumer-interface rule. Conversion (`reflect.Value(view)`) is
  zero-cost.
- **Appender contract**: `Append` snapshots `len(dst)` and rolls back to
  the prior length on a mid-encode failure, so callers never see a torn
  buffer.

## Performance (own-traversal; audit levers verified)

The encode path writes into a pre-sized pooled scratch buffer, so the
`-gcflags=-m=2` "escapes to heap" annotations on `encodeInt`/`encodeUint`'s
`append` calls are benign — the buffer has capacity, so no allocation
occurs at runtime (Marshal of a scalar is 3 allocs / 48 B, none from those
functions). Splitting them to satisfy the inline budget is churn with no
measurable win. A `decodeString` `unsafe.String` fast-path is also unsafe
here: decoded values outlive the `Unmarshal` call while the caller may
reuse the input `data`, so aliasing it risks a use-after-free — the
`string(rest[:length])` copy is correct and stays. The realised wins are
the cached `structTypeInfo`, inline-scalar dispatch, pre-computed name
prefixes, and typed root decode (Phases 6-8).

The encode scratch is a **local** `sync.Pool` of `*[]byte` (`scratchPool`,
`encoder.go`), capped at `maxRetainedScratchBytes = 256 << 10`. It is
deliberately NOT part of the shared `internal/core/codec/scratch`
mutualisation: that package pools `*bytes.Buffer` / `*bytes.Reader` (the shape
the ten library-mediated codecs share), whereas TLV's varint encoder grows a
raw `[]byte` and would gain nothing from a `bytes.Buffer` wrapper. The shared
`256 << 10` retain threshold is matched intentionally — it is the project-wide
oversize-discard ceiling, not copied pool boilerplate.

## Verification

```
bazel test --config=race //internal/service/codec/tlv:tlv_test
```
