# internal/core/transform

`transform` declares the byte-transform port of the SDK: the `Compressor`
contract and the process-wide registry mapping an `Algorithm` to a registered
`Compressor`. It is the fifth core sibling (ADR 0014) and mirrors
`internal/core/codec` exactly — the registry resolves an `Algorithm` to a
`Compressor` the way codec resolves a `Format` to a `Codec`.

## Port

```go
type Algorithm string // "gzip", "flate" — frozen per scheme, like codec.Format

type Compressor interface {
    Algorithm() Algorithm
    Compress(dst, src []byte) (out []byte, err error)
    Decompress(dst, src []byte) (out []byte, err error)
}

func Register(c Compressor) Compressor
func Lookup(a Algorithm) (Compressor, bool)
func Available() []Algorithm
```

Concrete schemes live under `internal/service/transform/` (stdlib gzip/flate)
and self-register via a package-level `var` at import — no `init()`.

## Why a parallel registry, not a codec Format

Adding `gzip` as a codec `Format` would break the frozen `any→[]byte` codec
contract and the append-only Format set. The compression verbs
(`MarshalCompressed` / `UnmarshalCompressed`) and the self-describing
compressed-frame format live at `pkg/v1/codec`; this package declares only the
port + registry. See ADR 0014 D1.

## Error codes (range `0.2.5.*`)

| Code      | Var                      | Trigger |
|---|---|---|
| `0.2.5.1` | `UnknownCompressor`      | Lookup/Decompress of an unregistered algorithm |
| `0.2.5.2` | `CompressionFailed`      | the compressor returned an error |
| `0.2.5.3` | `DecompressionFailed`    | the decompressor returned an error |
| `0.2.5.4` | `CompressedFrameInvalid` | malformed frame OR decompression-bomb guard tripped (emitter: `pkg/v1/codec` frame layer) |
