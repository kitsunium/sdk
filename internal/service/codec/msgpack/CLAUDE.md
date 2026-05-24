<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/codec/msgpack/

## Purpose

MessagePack codec wrapping `github.com/vmihailenco/msgpack/v5`. Streaming Encoder/Decoder are forwarded directly.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"msgpack"` |
| `MIMETypes()`    | `application/msgpack`, `application/x-msgpack` |
| `Extensions()`   | `.msgpack`, `.mpk` |
| Constructor      | `New() codec.Codec` |
| Streaming        | yes (`NewEncoder`, `NewDecoder`) |
| Appender         | not implemented |

## Error codes (range `0.3.7.*`)

| Code         | Var               | Trigger |
|---|---|---|
| `0.3.7.1`    | `MarshalFailed`   | `vmihailenco/msgpack/v5.Marshal` returned an error |
| `0.3.7.2`    | `UnmarshalFailed` | `Unmarshal` failed OR input exceeded `maxMsgPackBytes` (10 MiB) |

## Conventions

- **Byte cap on `Unmarshal`**: `maxMsgPackBytes = 10 << 20` (10 MiB). vmihailenco/msgpack v5 exposes no per-decoder caps for array / map / nesting depth; a MessagePack value with a huge declared length pre-allocates that many slots before any consistency check, so size-limiting the input buffer is the primary defence against memory-exhaustion DoS (CWE-400 / CWE-1284).
- Over-cap rejection carries diagnostic `Fields`: `len`, `cap` — surfaced via `errs.Int(...)`.
- Stateless singleton.

## Performance (lib-bound)

vmihailenco/msgpack/v5 performs its reflect walk internally. The realised
wins are GetEncoder/GetDecoder reuse from the library's own pools,
`UseCompactInts(true)` for narrower int wire forms, and encoding into a
pooled buffer + reader (now `internal/core/codec/scratch`). The library's
reflect walk is not reachable from this layer; do not re-add a local pool.

## Verification

```
bazel test --config=race //internal/service/codec/msgpack:msgpack_test
```
