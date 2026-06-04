<!-- updated: 2026-05-18T14:30:00Z -->
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
- `Append` delegates to `Marshal` so the wrap/error contract has a single source; on failure the original `dst` is returned untouched and the wrapped error surfaces with `CodeJSONMarshalFailed`.
- Streaming encoder/decoder wrap the stdlib types only to bridge the `core/codec.Encoder.Close` and `core/codec.Decoder.More` shape.

## Performance (lib-bound)

`encoding/json`'s reflect walk and `typeFields` cache are internal to the
stdlib — unreachable from this layer. The realised wins are the
`json.RawMessage` pass-through fast-path (Marshal + Append skip the reflect
round-trip when the bytes already exist) and Append encoding into a pooled
buffer (now `internal/core/codec/scratch`). `encoding/json/v2` (GOEXPERIMENT)
would be the only further lever and is ADR-gated. Do not re-add a local pool.

## Verification

```
bazel test --config=race //internal/service/codec/json:json_test
```

## Accepted audit findings

- Deferred/accepted low+info audit findings (V49, V51) are recorded in `.claude/contexts/sdk-audit-2026-06-03-accepted.yaml` (2026-06-03 close-out). Each is a deliberate decision or deferred change, not an open bug.
