# `pkg/v1/codec`

**Layer**: public facade · **Scope**: universal encoder/decoder dispatch

One verb, eighteen formats. `codec.Marshal(format, v)` and `codec.Unmarshal(data, &v)` reach every encoding the SDK ships — JSON, YAML, CBOR, MessagePack, NDJSON, XML, TOML, CSV, ASN.1 DER, PEM, TLV, FlatBuffers, plus the six base-N variants (`base64`, `base64url`, `base32`, `base16`, `hex`, `ascii85`). Format-swap at runtime is a single string change.

Concrete codecs live in `internal/service/codec/*`; this package is the read-only dispatch facade. Importing the package activates every codec via blank-import side effects.

## Surface

```go
// Dispatch — pick the codec by registered Format name.
func Marshal(format Format, v any) ([]byte, error)
func Unmarshal(data []byte, v any) error  // resolves Format from caller's choice

// Streaming — when the resolved codec satisfies core/codec.StreamingCodec.
func NewEncoder(format Format, w io.Writer) (Encoder, error)
func NewDecoder(format Format, r io.Reader) (Decoder, error)

// Registry introspection.
func Available() []Format               // every registered Format, sorted
func FromMIME(mime string)      (Format, bool)
func FromExtension(ext string)  (Format, bool)
```

`Format` is `type Format = string` so callers can use the named constants (`codec.JSON`, `codec.CBOR`, …) or the literal Format name registered by the service codec (`"base64"`, `"flatbuffers"`, …). Registered Format names are frozen post-v1.0.0.

## Quick start

```go
import (
    "fmt"

    "github.com/kitsunium/sdk/pkg/v1/codec"
)

type User struct {
    Name string `json:"name" cbor:"name" yaml:"name"`
    Age  int    `json:"age"  cbor:"age"  yaml:"age"`
}

func main() {
    u := User{Name: "Ada", Age: 36}

    // Same verb for any registered format.
    for _, f := range []codec.Format{"json", "cbor", "yaml", "msgpack", "base64"} {
        data, _ := codec.Marshal(f, u)
        var back User
        _ = codec.Unmarshal(data, &back)
        fmt.Printf("%-10s %d bytes  → %#v\n", f, len(data), back)
    }
}
```

## Choosing a Format

| Format | Best for | Notes |
|---|---|---|
| `json`        | API responses, configs | Most-supported; reasonable size |
| `ndjson`      | Streaming logs, line-oriented batches | Appender + line framing |
| `yaml`        | Human-edited configs | Slower; not great at scale |
| `toml`        | App configs | Strict typing |
| `xml`         | Legacy integrations | Verbose; specialised payloads |
| `csv`         | Tabular exports | Rows in / rows out; not arbitrary structs |
| `cbor`        | Binary IoT / mobile | Compact + fast |
| `msgpack`     | RPC payloads | Compact; ecosystem-wide |
| `tlv`         | Custom binary streams | Self-describing, reflection-driven, hardened |
| `flatbuffers` | Zero-copy passthrough | Schema lives outside the codec |
| `asn1-der`    | Crypto / X.509 artefacts | Strict DER rules |
| `pem`         | Crypto / certificates | Block-wrapped DER |
| `base64` / `base64url` / `base32` / `base16` / `hex` / `ascii85` | Wrap any structure in a text-safe encoding | Pipeline = `encoding/json.Marshal(v)` → base-N. Use stdlib `encoding/base64` (etc.) directly when you have raw bytes already. |

## Extension interfaces

A registered codec MAY implement one or both of these optional interfaces (assert at the call site):

```go
// Append into a caller-supplied buffer — zero-alloc fast path.
type Appender interface {
    Append(dst []byte, v any) ([]byte, error)
}

// Streaming — open Encoder/Decoder around an io.Writer/io.Reader.
type StreamingCodec interface {
    NewEncoder(w io.Writer) Encoder
    NewDecoder(r io.Reader) Decoder
}
```

Sentinels signal when a feature is unsupported (`codec.StreamingUnsupported` from `NewEncoder` / `NewDecoder` when the resolved codec is not streaming-capable).

## Errors

Dispatch failures carry typed dotted-quad codes under range `1.2.0.*`:

- `1.2.0.1` `CodeUnknownFormat` — `codec.Available()` does not list the requested Format.
- `1.2.0.2` `CodeCodecUnavailable` — reserved for future build-tag gating.
- `1.2.0.3` `CodeStreamingUnsupported` — `NewEncoder` / `NewDecoder` called on a non-streaming codec.

Per-codec failures carry the codec's own range (`0.3.*` for service codecs; see `docs/adr/0005-sdk-error-codes-dotted-quad.md` and `docs/adr/0006-sdk-error-code-registry-extension.md`). Inspect with `pkg/v1/errs.HasCode(err, codeXxx)` or `errs.HasReason(err, "REASON")`.

## What is NOT here

- **`codec.Register`** is not re-exported. Registration is `init()`-driven by service codec packages; consumers do not register codecs at runtime.
- **No constructors for codec internals.** `Encoder` / `Decoder` are interfaces; concrete types live in `internal/service/codec/*`.
- **No raw byte-level base-N surface.** For non-JSON-wrapped base-N encoding, call stdlib `encoding/base64`, `encoding/base32`, `encoding/hex`, or `encoding/ascii85` directly — the SDK does not ship a parallel byte-level API (uniformity rule, see `pkg/CLAUDE.md`).

## Performance

Benchmarks for every registered Format × payload size × operation are checked in under `BENCH.md` files within each codec package. Run locally with:

```shell
# Regenerate every BENCH.md across the SDK (recommended — stamps a
# reproducibility envelope at the top of each report):
make bench

# Or run codec benchmarks ad-hoc, output to stdout:
bazel test //pkg/v1/codec:codec_bench_test \
    --test_arg=-test.bench=. \
    --test_arg=-test.benchmem \
    --test_arg=-test.run=^$ \
    --test_arg=-test.benchtime=2s \
    --test_output=streamed
```

The bench target is tagged `manual` so it is excluded from `bazel test //...` and never runs on PR CI by default.

## See also

- `docs/adr/0003-sdk-codec-package.md` — universal codec ADR
- `pkg/v1/codec/CLAUDE.md` — agent-facing operating notes
- `internal/service/codec/*/CLAUDE.md` — per-codec hardening + error ranges
- `pkg/v1/errs/README.md` — error introspection contract
