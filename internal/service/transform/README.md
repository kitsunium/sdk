# internal/service/transform

Concrete `core/transform.Compressor` implementations over the Go standard
library: **gzip** (`compress/gzip`), **flate** (raw DEFLATE, `compress/flate`)
and **zlib** (the RFC 1950 envelope, `compress/zlib`). One package, three
self-registering schemes.

```go
var GzipCompressor  = transform.Register(gzipCompressor{})  // Algorithm "gzip"
var FlateCompressor = transform.Register(flateCompressor{}) // Algorithm "flate"
var ZlibCompressor  = transform.Register(zlibCompressor{level: zlib.DefaultCompression}) // Algorithm "zlib"
```

Blank-importing the package registers all three with `internal/core/transform`,
so a consumer that only needs `transform.Lookup("gzip")` adds a single blank
import.

## `flate` is not `deflate`

| Scheme | RFC | Wire shape |
|---|---|---|
| `flate` | RFC 1951 | bare DEFLATE bit-stream — no header, no checksum |
| `zlib`  | RFC 1950 | 2-byte header + the same DEFLATE body + Adler-32 trailer |
| `gzip`  | RFC 1952 | 10-byte header + DEFLATE body + CRC-32 and size trailer |

HTTP's `Content-Encoding: deflate` (RFC 9110 §8.4.1) names the **zlib
envelope**, not the raw DEFLATE stream. A peer advertising `deflate` wants
`zlib`; `flate` is correct only where the surrounding format supplies the
framing. The two are not wire-compatible in either direction and carry distinct
error codes.

## Compression level

`NewZlibCompressor(level)` builds a zlib compressor at an explicit level and
clamps to `zlib.DefaultCompression` anything that would not compress — both a
level the stdlib writer rejects and `zlib.NoCompression`, which it accepts and
which stores bytes verbatim. No configuration yields a compressor that silently
does not compress (ADR 0031). The result is not registered; the registry entry
is the default-level `ZlibCompressor`.

## Bounded decompression

`Decompress` drains its stdlib reader through `io.LimitReader` capped at 256 MiB
(`maxDecompressedBytes`). An over-cap stream returns the scheme's failure
sentinel instead of driving an OOM — the conservative layer-local backstop. The
full ratio-aware decompression-bomb guard lives in the `pkg/v1/codec` frame
layer (a later commit). This package ships **stdlib only**; the vendor
compressors (zstd, s2) live in `third-party/transform` behind their own opt-in
import, with their own ceiling (ADR 0066).

## Error codes (range `0.3.26.*`)

| Code       | Var          | Trigger |
|---|---|---|
| `0.3.26.1` | `GzipFailed`  | `compress/gzip` returned an error (Compress or Decompress) |
| `0.3.26.2` | `FlateFailed` | `compress/flate` returned an error (Compress or Decompress) |
| `0.3.26.3` | `ZlibFailed`  | `compress/zlib` returned an error (Compress or Decompress), including a failed Adler-32 check |
