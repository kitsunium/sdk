<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/codec/asn1/

## Purpose

ASN.1 DER codec wrapping stdlib `encoding/asn1`. Emits DER exclusively (the stdlib produces nothing else); BER input is silently accepted on `Unmarshal`.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"asn1-der"` |
| `MIMETypes()`    | `application/pkix-cert` |
| `Extensions()`   | `.der`, `.cer` |
| Constructor      | `New() codec.Codec` (returns the registered singleton) |
| Streaming        | not implemented |
| Appender         | not implemented |

## Error codes (range `0.3.9.*`)

| Code         | Var               | Trigger |
|---|---|---|
| `0.3.9.1`    | `MarshalFailed`   | `encoding/asn1.Marshal` returned an error |
| `0.3.9.2`    | `UnmarshalFailed` | `encoding/asn1.Unmarshal` returned an error |

Both sentinels use Reason `MARSHAL_FAILED` / `UNMARSHAL_FAILED`.

## Conventions

- Stateless singleton; `New()` returns `Codec`.
- `Unmarshal` accepts trailing bytes past the first decoded structure (stdlib contract). If strict trailing-byte rejection is needed, add a dedicated `UnmarshalStrict` helper rather than tightening this default.

## Performance (lib-bound)

`encoding/asn1` drives Marshal/Unmarshal entirely through reflection
internal to the stdlib and exposes no append-style or buffer-reuse API, so
there is no reachable reflect-elimination or pooling lever at this layer —
`asn1.Marshal` allocates and returns a fresh `[]byte` we cannot intercept.
The codec is intentionally allocation-bound by its library; optimisation
would have to land upstream. (Verified during the codec-perf-micro-pprof
pass — do not re-attempt a local pool or reflect cache here.)

## Verification

```
bazel test --config=race //internal/service/codec/asn1:asn1_test
```
