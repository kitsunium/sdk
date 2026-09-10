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

## Cost

Measured in `BENCH.md`. The answer depends on **which shape of
message** you hand it, by a factor of 3.6 — and the shape most callers reach for
is the slow one.

| Marshal, ~200 fields | ns/op | B/op | allocs | wire |
|---|---:|---:|---:|---:|
| generated message | **38 768** | 5 376 | **1** | 5 116 B |
| `structpb.Struct` | 140 603 | 11 264 | **401** | 4 324 B |

With **generated types** protobuf is what it is sold as: **5.33× faster than
JSON and 2.76× smaller**, in one allocation (`proto.Marshal` sizes the message in
a first pass — 42.5 % of its CPU — then fills a perfectly sized buffer).

Through **`structpb.Struct` it loses on every axis**: 1.07–1.47× slower than
JSON, 3.3–4.2× slower than CBOR, and **18–29 % LARGER on the wire than JSON**,
because `structpb` puts the field names back and wraps every value in a
`Value` submessage. Worse, a caller holding a `map[string]any` must call
`structpb.NewStruct` first, and at 200 fields **that conversion alone (33 811 ns)
costs what CBOR takes to encode the whole document (33 758 ns), to within 0.2 %**
— end to end, 1.45× JSON and 5.17× CBOR.

`Unmarshal` costs **2.82× its own `Marshal`** on a generated message (109 288 ns,
1 676 allocations): the one-allocation encode is not matched by the decode, so
size a decode-heavy service on the second number.

`Append` is `Marshal` plus a copy — `proto` exposes no public append API — so it
**saves nothing**: identical `B/op` and `allocs/op`, measured.

**Choose this codec when the wire must be Protobuf and you have generated types.
`structpb` is an interoperability tool, not a serialisation strategy.**

## Do NOT

- Blank-import this into `pkg/v1/codec` — it would break the universal-round-trip
  contract (schema codecs are opt-in by design).
- Marshal a plain map/struct and expect success — Protobuf needs a message.
- Use `fmt.Errorf`/`errors.New`; wrap through `errs.Wrap`.

## Verification

```
bazel test --config=race //third-party/codec/protobuf:protobuf_test
```
