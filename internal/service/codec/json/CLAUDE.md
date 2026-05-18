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

## Verification

```
bazel test --config=race //internal/service/codec/json:json_test
```
