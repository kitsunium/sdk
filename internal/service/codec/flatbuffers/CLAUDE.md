<!-- updated: 2026-05-21T21:27:56Z -->
# internal/service/codec/flatbuffers/

## Purpose

Thin passthrough codec for already-encoded FlatBuffer payloads. The universal
`Codec(any)` contract cannot host schema-driven FlatBuffers access directly,
so this package only round-trips raw FlatBuffer bytes via the
`pkg/v1/codec.Marshal/Unmarshal` facade. Real schema-typed reads stay in the
caller's `flatc`-generated accessors.

No third-party dependency — pure stdlib.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"flatbuffers"` |
| `MIMETypes()`    | `application/x-flatbuffers`, `application/octet-stream+flatbuffers` |
| `Extensions()`   | `.fbs`, `.bin` (see Conventions: `.bin` is ambiguous) |
| Constructor      | `New() codec.Codec` |
| Streaming        | **not** implemented (no natural framing for a passthrough codec) |
| Appender         | yes (`Append(dst, v) ([]byte, error)`) |

## Adapter interfaces

| Interface | Used by | Purpose |
|---|---|---|
| `BytesProvider` (`Bytes() []byte`)     | Marshal / Append | lets generated FlatBuffer types expose their underlying buffer without exporting it as `[]byte` |
| `BytesAcceptor` (`SetBytes([]byte)`)   | Unmarshal        | lets generated FlatBuffer types accept the buffer with their own ownership semantics |

The `Provider` / `Acceptor` suffixes are registered as legitimate
adapter-role vocabulary in `/workspace/.ktn-linter.yaml`
(per kodflow/ktn-linter#337), so `KTN-INTERFACE-ERNAME` does not fire
on these names.

`Marshal` accepts `[]byte` directly OR any `BytesProvider`. `Unmarshal`
accepts `*[]byte` directly (zero-copy reference; caller takes shared
ownership) OR any `BytesAcceptor`.

## Error codes (range `0.3.23.*`)

| Code         | Var                     | Reason                  | Trigger |
|---|---|---|---|
| `0.3.23.1`   | `FlatbuffersBadType`    | `FLATBUFFERS_BAD_TYPE`   | Marshal value is neither `[]byte` nor a `BytesProvider` |
| `0.3.23.2`   | `FlatbuffersBadTarget`  | `FLATBUFFERS_BAD_TARGET` | Unmarshal target is neither `*[]byte` nor a `BytesAcceptor` |
| `0.3.23.3`   | `FlatbuffersTruncated`  | `FLATBUFFERS_TRUNCATED`  | Buffer shorter than the 4-byte root-offset header OR larger than `maxFlatBuffersBytes` (64 MiB) |

Reason strings are the screaming-snake of the (Go) variable name — the
SDK-wide AST audit enforces this, which is why `Flatbuffers` is a single
word (giving `FLATBUFFERS_*`) and the second-noun is `BadType`/`BadTarget`
rather than `UnsupportedType`/`UnsupportedTarget` (the latter would exceed
the `KTN-CONST-MAXLEN` 30-char cap on the const identifier).

## Conventions

- **Size cap**: `maxFlatBuffersBytes = 64 << 20` (64 MiB). FlatBuffers
  payloads tend to carry larger blobs than JSON (embedded media, tensor
  weights, …), so the cap is generous; the same CWE-400 / memory-exhaustion
  defence still applies. Callers with legitimate larger buffers should bypass
  this codec and consume the generated accessors directly.
- **Header-only validation**: the codec only checks the 4-byte root-offset
  header. It does NOT parse vtables, fields, or offsets — that responsibility
  stays with the caller's schema-typed accessors.
- **`.bin` extension is ambiguous**: many binary formats use `.bin`; consumers
  resolving by extension MUST also disambiguate by content if multiple
  binary codecs are registered.
- **Stateless singleton**: registered at package load via
  `var Codec codec.Codec = codec.Register(&flatbuffersCodec{})`; no `init()`.

## Verification

```shell
bazel test --config=race //internal/service/codec/flatbuffers:flatbuffers_test
```
