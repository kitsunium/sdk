<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/codec/yaml/

## Purpose

YAML codec wrapping `gopkg.in/yaml.v3`. Supports multi-document streams via `---` markers; streaming Encoder/Decoder are forwarded.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"yaml"` |
| `MIMETypes()`    | `application/yaml`, `text/yaml`, `application/x-yaml` |
| `Extensions()`   | `.yaml`, `.yml` |
| Constructor      | `New() codec.Codec` |
| Streaming        | yes (`NewEncoder`, `NewDecoder`) |
| Appender         | not implemented |

## Error codes (range `0.3.4.*`)

| Code         | Var               | Trigger |
|---|---|---|
| `0.3.4.1`    | `MarshalFailed`   | `gopkg.in/yaml.v3.Marshal` returned an error |
| `0.3.4.2`    | `UnmarshalFailed` | `Unmarshal` failed OR input exceeded `maxYAMLBytes` (10 MiB) |

## Conventions

- **Byte cap on `Unmarshal`**: `maxYAMLBytes = 10 << 20` (10 MiB). yaml.v3 caps alias expansion internally (post-3.0.0) but has no upstream byte budget; a single huge document still pulls the whole buffer into memory before any structural check (CWE-400 / CWE-776). Streaming callers that legitimately need larger inputs use `NewDecoder` plus their own `io.LimitReader` sizing — the byte cap applies to `Unmarshal` only.
- Over-cap rejection carries diagnostic `Fields`: `len`, `cap` — surfaced via `errs.Int(...)`.
- Stateless singleton.

## Performance (lib-bound)

gopkg.in/yaml.v3 performs its reflect walk internally. The realised wins are
`SetIndent(2)` (≈50% smaller output on nested configs, hence less buffer
growth) and pooling the outer *bytes.Buffer — now mutualised via
`internal/core/codec/scratch`. The library's reflection is unreachable; do
not re-add a local pool.

## Verification

```
bazel test --config=race //internal/service/codec/yaml:yaml_test
```
