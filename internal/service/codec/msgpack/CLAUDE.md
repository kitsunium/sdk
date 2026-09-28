<!-- updated: 2026-09-28T19:19:15Z -->
# internal/service/codec/msgpack/

## Purpose

MessagePack codec wrapping `github.com/vmihailenco/msgpack/v5`. Streaming Encoder/Decoder are forwarded; the decoder is held to the same byte cap as `Unmarshal`.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"msgpack"` |
| `MIMETypes()`    | `application/msgpack`, `application/x-msgpack` |
| `Extensions()`   | `.msgpack`, `.mpk` |
| Constructor      | `New() codec.Codec` |
| Streaming        | yes (`NewEncoder`, `NewDecoder`) |
| Appender         | yes (`Append(dst, v) ([]byte, error)`) — pooled encoder + buffer (UseCompactInts), then appends onto dst |

## Error codes (range `0.3.7.*`)

| Code         | Var               | Trigger |
|---|---|---|
| `0.3.7.1`    | `MarshalFailed`   | `vmihailenco/msgpack/v5.Marshal` returned an error |
| `0.3.7.2`    | `UnmarshalFailed` | `Unmarshal` failed OR input exceeded `maxMsgPackBytes` (10 MiB) |

## Conventions

- **Byte cap on `Unmarshal`**: `maxMsgPackBytes = 10 << 20` (10 MiB). vmihailenco/msgpack v5 exposes no per-decoder caps for array / map / nesting depth; a MessagePack value with a huge declared length pre-allocates that many slots before any consistency check, so size-limiting the input buffer is the primary defence against memory-exhaustion DoS (CWE-400 / CWE-1284).
- **The streaming decoder inherits the cap**: `NewDecoder` reads through `io.LimitReader(r, maxMsgPackBytes+1)`, so the library never reads more than `maxMsgPackBytes + 1` bytes of one stream — the whole stream, not each value — and a value cut at the limit surfaces as `UNMARSHAL_FAILED` from `Decode`. Pinned by `Test_msgpackCodec_NewDecoder_StreamingDoS`.
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
