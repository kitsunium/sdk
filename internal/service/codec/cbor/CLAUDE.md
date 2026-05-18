<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/codec/cbor/

## Purpose

CBOR codec wrapping `github.com/fxamacker/cbor/v2`. Streams individual CBOR items via Encoder/Decoder.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"cbor"` |
| `MIMETypes()`    | `application/cbor` |
| `Extensions()`   | `.cbor` |
| Constructor      | `New() codec.Codec` |
| Streaming        | yes (`NewEncoder`, `NewDecoder`) |
| Appender         | not implemented |

## Error codes (range `0.3.6.*`)

| Code         | Var               | Trigger |
|---|---|---|
| `0.3.6.1`    | `MarshalFailed`   | `cbor/v2.Marshal` returned an error |
| `0.3.6.2`    | `UnmarshalFailed` | `cbor/v2.Unmarshal` (hardened DecMode) returned an error |

## Conventions

- **Hardened `DecMode`**: every `Unmarshal` routes through `decMode := mustHardenedDecMode()`, built at package load with `MaxArrayElements=1<<20`, `MaxMapPairs=1<<20`, `MaxNestedLevels=32`. Defaults match fxamacker's README "Security Tips".
- `mustHardenedDecMode` panics on the defensive error branch — `DecOptions.DecMode()` only fails on self-contradictory options, so a panic at package load is preferable to silent misconfiguration at runtime.
- Stateless; `New()` returns the registered singleton.

## Verification

```
bazel test --config=race //internal/service/codec/cbor:cbor_test
```
