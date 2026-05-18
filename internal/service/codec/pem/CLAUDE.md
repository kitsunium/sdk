<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/codec/pem/

## Purpose

PEM codec wrapping stdlib `encoding/pem`. PEM is block-structured (`-----BEGIN CERTIFICATE-----` … `-----END …-----`), so the codec operates on `*pem.Block` values.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"pem"` |
| `MIMETypes()`    | `application/x-pem-file` (no IANA registration; this is the de-facto type) |
| `Extensions()`   | `.pem`, `.crt`, `.key` |
| Constructor      | `New() codec.Codec` |
| Streaming        | **not** implemented (PEM blocks are atomic; multi-block streams should be decoded by looping `pem.Decode` in the caller) |
| Appender         | not implemented |
| Type alias       | `pem.Block = stdpem.Block` (re-export so consumers do not need to import `encoding/pem` directly) |

## Error codes (range `0.3.10.*`)

| Code         | Var               | Trigger |
|---|---|---|
| `0.3.10.1`   | `MarshalFailed`   | `encoding/pem.Encode` returned an error |
| `0.3.10.2`   | `UnmarshalFailed` | `encoding/pem.Decode` returned a nil block (malformed input) |
| `0.3.10.3`   | `ValueInvalid`    | Marshal input is not `*pem.Block` (or nil); Unmarshal target is not `**pem.Block` (or nil) |

## Conventions

- **Block-typed interface**: Marshal accepts `*pem.Block`, Unmarshal accepts `**pem.Block`. Anything else fails with `CodePEMValueInvalid` — no automatic coercion.
- Decode returns the **first** block; trailing bytes are silently ignored. Multi-block streams (cert chains) require a caller loop.
- Stateless singleton.

## Verification

```
bazel test --config=race //internal/service/codec/pem:pem_test
```
