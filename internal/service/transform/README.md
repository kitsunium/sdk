# internal/service/transform

Concrete `core/transform.Compressor` implementations over the Go standard
library: **gzip** (`compress/gzip`) and **flate** (raw DEFLATE,
`compress/flate`). One package, two self-registering schemes.

```go
var GzipCompressor  = transform.Register(gzipCompressor{})  // Algorithm "gzip"
var FlateCompressor = transform.Register(flateCompressor{}) // Algorithm "flate"
```

Blank-importing the package registers both with `internal/core/transform`, so a
consumer that only needs `transform.Lookup("gzip")` adds a single blank import.

## Bounded decompression

`Decompress` drains its stdlib reader through `io.LimitReader` capped at 256 MiB
(`maxDecompressedBytes`). An over-cap stream returns the scheme's failure
sentinel instead of driving an OOM — the conservative layer-local backstop. The
full ratio-aware decompression-bomb guard lives in the `pkg/v1/codec` frame
layer (a later commit). The first wave ships **stdlib only**; zstd / snappy / s2
are deferred.

## Error codes (range `0.3.26.*`)

| Code       | Var          | Trigger |
|---|---|---|
| `0.3.26.1` | `GzipFailed`  | `compress/gzip` returned an error (Compress or Decompress) |
| `0.3.26.2` | `FlateFailed` | `compress/flate` returned an error (Compress or Decompress) |
