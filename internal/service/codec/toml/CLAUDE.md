<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/codec/toml/

## Purpose

TOML codec wrapping `github.com/pelletier/go-toml/v2`. Streaming Encoder/Decoder are forwarded.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"toml"` |
| `MIMETypes()`    | `application/toml` |
| `Extensions()`   | `.toml` |
| Constructor      | `New() codec.Codec` |
| Streaming        | yes (`NewEncoder`, `NewDecoder`) |
| Appender         | not implemented |

## Error codes (range `0.3.5.*`)

| Code         | Var               | Trigger |
|---|---|---|
| `0.3.5.1`    | `MarshalFailed`   | `pelletier/go-toml/v2.Marshal` returned an error |
| `0.3.5.2`    | `UnmarshalFailed` | `pelletier/go-toml/v2.Unmarshal` returned an error |

## Conventions

- Stateless singleton.
- No additional hardening beyond pelletier's own input validation; TOML's grammar inherently bounds document complexity.

## Performance (lib-bound)

pelletier/go-toml/v2 performs its reflect walk internally and exposes no
encoder-buffer-reset hook, so the only lever at this layer is pooling the
outer *bytes.Buffer that Marshal/Append encode into — now mutualised via
`internal/core/codec/scratch`. The library's reflection is unreachable; do
not re-add a local pool.

## Verification

```
bazel test --config=race //internal/service/codec/toml:toml_test
```
