# internal/service/codec/bson/

## Purpose

BSON codec — wraps `go.mongodb.org/mongo-driver/bson` behind the universal
`core/codec.Codec` dispatch. BSON is MongoDB's binary document format. Like
the other library-backed codecs (cbor/msgpack/toml/yaml) the third-party
dependency lives in `internal/service/go.mod`; blank-importing this package
self-registers the `"bson"` Format (no `init()`). ADR 0021.

## Surface

| Aspect | Value |
|---|---|
| Format name | `"bson"` |
| MIME types | `application/bson` |
| Extensions | `.bson` |
| Streaming | **no** (mongo-driver exposes no stable io-stream Encoder/Decoder) |
| Appender | yes (`Marshal` + `append`; no in-place library API) |
| Code range | `0.3.36.*` (ADR 0021) |

## Conventions

- **Top-level must be a document.** BSON cannot represent a bare scalar at the
  root, so `Marshal` of a top-level `int`/`string`/etc. returns
  `BSON_MARSHAL_FAILED` (the library error). Round-trip a struct or `map`.
- **Hard cap on Unmarshal**: 10 MiB (`maxBSONBytes`) — CWE-400 defence before
  the decoder reads declared element lengths and pre-allocates.
- **Stateless singleton** — `New()` returns the registered singleton;
  `MIMETypes`/`Extensions` return `slices.Clone` copies.
- **Errors** carry the package's dotted-quad codes (`codes.go`) via
  `errs.Wrap`; sentinels in `errors.go`. No `fmt.Errorf`.

## Error codes (range `0.3.36.*`)

| Code | Var | Trigger |
|---|---|---|
| `0.3.36.1` | `MarshalFailed` | `bson.Marshal` failed (e.g. top-level scalar) |
| `0.3.36.2` | `UnmarshalFailed` | `bson.Unmarshal` rejected the input |
| `0.3.36.3` | `SizeExceeded` | `len(data)` exceeds `maxBSONBytes` (10 MiB) |

## Do NOT

- Marshal a top-level scalar and expect success — BSON requires a document.
- Add an `init()` — registration is the package-level `var Codec = codec.Register(...)`.
- Use `fmt.Errorf`/`errors.New`; wrap library failures through `errs.Wrap`.

## Verification

```
bazel test --config=race //internal/service/codec/bson:bson_test
```
