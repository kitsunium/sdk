# third-party/codec/protobuf/

## Purpose

Protobuf codec — wraps `google.golang.org/protobuf` behind the universal
`core/codec.Codec` dispatch. **Lives under `third-party/codec/` (root module),
NOT `internal/service/codec`**, and is **opt-in** (not blank-imported by
`pkg/v1/codec`). The reason is not dep weight (protobuf is light) but the
**schema-bound contract**: Protobuf can only encode `proto.Message` values, so
it cannot honour the universal "every registered codec round-trips any Go
struct" guarantee that `pkg/v1/codec`'s `TestUniversalRoundtripAllCodecs`
enforces. Schema codecs are therefore opt-in: a consumer blank-imports
`…/third-party/codec/protobuf` to register the `"protobuf"` Format. ADR 0023.

## Surface

| Aspect | Value |
|---|---|
| Format name | `"protobuf"` |
| MIME types | `application/protobuf`, `application/x-protobuf` |
| Extensions | `.pb` |
| Streaming | **no** (wire format is length-unframed) |
| Appender | yes (`Marshal` + `append`) |
| Code range | `0.3.38.*` (ADR 0023) |

## Conventions

- **Value must satisfy `proto.Message`.** A non-message value returns
  `PROTOBUF_MARSHAL_FAILED` (Marshal) / `PROTOBUF_UNMARSHAL_FAILED` (Unmarshal).
  Round-trip a generated message or a well-known type such as `structpb.Struct`
  (the test fixture — no codegen required).
- **Hard cap on Unmarshal**: 10 MiB (`maxProtobufBytes`, CWE-400).
- **Stateless singleton**; `MIMETypes`/`Extensions` return `slices.Clone` copies.
- **Errors** carry the dotted-quad codes via `errs.Wrap`; no `fmt.Errorf`.

## Error codes (range `0.3.38.*`)

| Code | Var | Trigger |
|---|---|---|
| `0.3.38.1` | `MarshalFailed` | non-`proto.Message` value, or `proto.Marshal` error |
| `0.3.38.2` | `UnmarshalFailed` | non-`proto.Message` target, or `proto.Unmarshal` error |
| `0.3.38.3` | `SizeExceeded` | `len(data)` exceeds `maxProtobufBytes` (10 MiB) |

## Do NOT

- Blank-import this into `pkg/v1/codec` — it would break the universal-round-trip
  contract (schema codecs are opt-in by design).
- Marshal a plain map/struct and expect success — Protobuf needs a message.
- Use `fmt.Errorf`/`errors.New`; wrap through `errs.Wrap`.

## Verification

```
bazel test --config=race //third-party/codec/protobuf:protobuf_test
```
