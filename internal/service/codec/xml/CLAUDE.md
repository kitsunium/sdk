<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/codec/xml/

## Purpose

XML codec wrapping stdlib `encoding/xml`. Streaming Encoder/Decoder are forwarded.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"xml"` |
| `MIMETypes()`    | `application/xml`, `text/xml` |
| `Extensions()`   | `.xml` |
| Constructor      | `New() codec.Codec` |
| Streaming        | yes (`NewEncoder`, `NewDecoder`) |
| Appender         | not implemented |

## Error codes (range `0.3.3.*`)

| Code         | Var               | Trigger |
|---|---|---|
| `0.3.3.1`    | `MarshalFailed`   | `encoding/xml.Marshal` returned an error |
| `0.3.3.2`    | `UnmarshalFailed` | `encoding/xml.Unmarshal` returned an error |

## Conventions

- **Billion-Laughs bounded** by stdlib: `encoding/xml` does not expand external entities and rejects DOCTYPE declarations, so the classic XXE / entity-expansion DoS vector is inert. Asserted by `TestUnmarshal_BillionLaughsBounded` in `codec_external_test.go`. No additional hardening is layered on top.
- Stateless singleton.

## Verification

```
bazel test --config=race //internal/service/codec/xml:xml_test
```
