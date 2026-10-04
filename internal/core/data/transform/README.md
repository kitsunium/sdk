# internal/core/data/transform

`transform` declares the byte-transform port of the SDK: the `Compressor`
contract and the process-wide registry mapping an `Algorithm` to a registered
`Compressor`. It is the fifth core sibling (ADR 0014) and mirrors
`internal/core/data/codec` exactly — the registry resolves an `Algorithm` to a
`Compressor` the way codec resolves a `Format` to a `Codec`.

## Port

```go
type Algorithm string // "gzip", "flate", "zlib" — frozen per scheme, like codec.Format

type Compressor interface {
    Algorithm() Algorithm
    Compress(dst, src []byte) (out []byte, err error)
    Decompress(dst, src []byte) (out []byte, err error)
}

func Register(c Compressor) Compressor
func Lookup(a Algorithm) (Compressor, bool)
func Available() []Algorithm
```

Concrete schemes live under `internal/service/data/transform/` (stdlib
gzip/flate/zlib) and self-register via a package-level `var` at import — no
`init()`. `flate` is the raw DEFLATE stream (RFC 1951); `zlib` is the RFC 1950
envelope that HTTP misnames `deflate`. They are distinct schemes, not aliases.

## Why a parallel registry, not a codec Format

Adding `gzip` as a codec `Format` would break the frozen `any→[]byte` codec
contract and the append-only Format set. The compression verbs
(`MarshalCompressed` / `UnmarshalCompressed`) and the self-describing
compressed-frame format live at `pkg/v1/data/codec`; this package declares only the
port + registry. See ADR 0014 D1.

## Error codes (range `0.2.5.*`)

| Code      | Var                      | Trigger |
|---|---|---|
| `0.2.5.1` | `UnknownCompressor`      | Lookup/Decompress of an unregistered algorithm |
| `0.2.5.2` | `CompressionFailed`      | the compressor returned an error |
| `0.2.5.3` | `DecompressionFailed`    | the decompressor returned an error |
| `0.2.5.4` | `CompressedFrameInvalid` | malformed frame OR decompression-bomb guard tripped (emitter: `pkg/v1/data/codec` frame layer) |

The stdlib schemes' three sentinels are declared here too since ADR 0160,
under the range their layer allocated:

| Code       | Var           | Trigger |
|---|---|---|
| `0.3.26.1` | `GzipFailed`  | `compress/gzip` returned an error (Compress or Decompress) |
| `0.3.26.2` | `FlateFailed` | `compress/flate` returned an error (Compress or Decompress) |
| `0.3.26.3` | `ZlibFailed`  | `compress/zlib` returned an error, including a failed Adler-32 check |

The ports — `Compressor` and `BoundedDecompressor` — are generated from
`design/data/transform.yaml` into `design_gen.go` (ADR 0163): a port changes in
the design, then `kit gen`.
