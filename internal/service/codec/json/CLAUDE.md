<!-- updated: 2026-09-28T19:19:15Z -->
# internal/service/codec/json/

## Purpose

JSON codec wrapping stdlib `encoding/json`. Reference implementation for the registry: stateless, streaming, and Appender-capable.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"json"` |
| `MIMETypes()`    | `application/json`, `text/json` |
| `Extensions()`   | `.json` |
| Constructor      | `New() codec.Codec` |
| Streaming        | yes (`NewEncoder`, `NewDecoder`) |
| Appender         | yes (`Append(dst, v) ([]byte, error)`) |

## Error codes (range `0.3.2.*`)

| Code         | Var               | Trigger |
|---|---|---|
| `0.3.2.1`    | `MarshalFailed`   | `encoding/json.Marshal` returned an error |
| `0.3.2.2`    | `UnmarshalFailed` | `encoding/json.Unmarshal` returned an error |

## Conventions

- Stateless singleton.
- `Append` shares `Marshal`'s `json.RawMessage` fast-path; any other value is encoded into a pooled buffer (`json.NewEncoder`, its trailing newline dropped) and copied once onto `dst`. On failure the original `dst` is returned untouched and the wrapped error surfaces with `CodeJSONMarshalFailed`.
- Streaming encoder/decoder wrap the stdlib types only to bridge the `core/codec.Encoder.Close` and `core/codec.Decoder.More` shape.

## Performance (lib-bound)

`encoding/json`'s reflect walk and its per-type field cache are internal to the
stdlib — unreachable from this layer. The realised wins are the
`json.RawMessage` pass-through fast-path (Marshal + Append skip the reflect
round-trip when the bytes already exist) and Append encoding into a pooled
buffer (now `internal/core/codec/scratch`). `encoding/json/v2` is the only
further lever: Go 1.27 ships it without an experiment flag and builds
`encoding/json` itself on it (`jsonv2` is in the toolchain's baseline), and
ADR 0102 pulled the lever for `strictjson` alone — this codec keeps the v1 API,
so `codec.Unmarshal(codec.JSON, …)` keeps its meaning. Do not re-add a local
pool.

## Verification

```
bazel test --config=race //internal/service/codec/json:json_test
```
