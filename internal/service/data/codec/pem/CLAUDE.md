<!-- updated: 2026-11-03T22:00:00Z -->
# internal/service/data/codec/pem/

## Purpose

PEM codec wrapping stdlib `encoding/pem`. PEM is block-structured (`-----BEGIN CERTIFICATE-----` … `-----END …-----`), so the codec operates on `*pem.Block` values.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"pem"` |
| `MIMETypes()`    | `application/x-pem-file` (no IANA registration; this is the de-facto type) |
| `Extensions()`   | `.pem`, `.crt`, `.key` |
| Constructor      | `New() codec.Codec` |
| Streaming        | yes (#901) — `NewDecoder` hands one `*pem.Block` per `Decode` into a `**pem.Block`: the blocks a loop of `pem.Decode` over the whole stream finds, read without holding the stream (`decoder.go`); `NewEncoder` writes one block per `Encode` (`encoder.go`) |
| Appender         | yes (`Append(dst, v) ([]byte, error)`) — the block is written into `dst` itself by the package's own encoder (`appendBlock`), grown once to the exact size when it lacks the room; `dst` returned as given on a refusal (#1124) |
| Type alias       | `pem.Block = stdpem.Block` (re-export so consumers do not need to import `encoding/pem` directly) |

## Error codes (range `0.3.10.*`)

Declared in `internal/core/data/codec/pem` — `codes.go` and `errors.go` — and used
here as `corepem.<Var>` (ADR 0160 §2: a code lives in the core at the path that
mirrors the package emitting it). The values, reasons and texts are the ones this
package always emitted; only the declaration moved.

| Code         | Var               | Trigger |
|---|---|---|
| `0.3.10.1`   | `MarshalFailed`   | a header key holds a colon — the one block `encoding/pem.Encode` refuses, checked as it checks it, before any output |
| `0.3.10.2`   | `UnmarshalFailed` | `encoding/pem.Decode` returned a nil block (malformed input) |
| `0.3.10.3`   | `ValueInvalid`    | Marshal input is not `*pem.Block` (or nil); Unmarshal target is not `**pem.Block` (or nil) |
| `0.3.10.4`   | `SizeExceeded`    | the stream decoder holds more than `maxStreamBlockBytes` (10 MiB) without a whole END line — refused, and the stream read no further |

## Conventions

- **`Sniff` (`sniff.go`, #909)** implements `core/data/codec.Sniffer`, which `codec.Detect` asks about an input's first 512 bytes: whitespace, then `-----BEGIN `, the pre-encapsulation boundary. It reads only the prefix and allocates nothing.
- **Block-typed interface**: Marshal accepts `*pem.Block`, Unmarshal accepts `**pem.Block`. Anything else fails with `CodePEMValueInvalid` — no automatic coercion.
- `Unmarshal` returns the **first** block; trailing bytes are silently ignored. A multi-block file (a certificate chain) is read with the stream decoder.
- **The stream decoder follows `pem.Decode`'s own rule** (Go 1.27): a block is decided at the first `"\n-----END "` after the last BEGIN line, so the decoder reads until an END line is whole — its `'\n'` read, or the stream ended — hands what it holds up to that line to `pem.Decode`, and keeps the block's rest; when nothing decodes, it drops what precedes that END marker, where `pem.Decode` itself resumes. Text between blocks is skipped as `pem.Decode` skips it. `FuzzStreamFindsWhatADecodeLoopFinds` reads any input one byte at a time and holds the blocks equal to a `pem.Decode` loop's. A stream that ends with a BEGIN line and no END is `UnmarshalFailed`; one with no block at all is `io.EOF`.
- **The encoder is the package's own, and it writes `encoding/pem`'s bytes.** `appendBlock` writes the BEGIN line, the headers (Proc-Type first, the others sorted), the body in base64 lines of 64 characters and the END line straight into the destination; `blockLen` sizes it exactly, so `Marshal` allocates its result once and `Append` nothing when `dst` has the room. `TestAppendBlockIsEncodingPEM` and `FuzzAppendBlockIsEncodingPEM` pin it to `encoding/pem.EncodeToMemory` byte for byte; the promotion shape (`JSON`, no headers) is no longer a separate path. `BENCH.md` has the figures.
- Stateless singleton.

## Verification

```
bazel test --config=race //internal/service/data/codec/pem:pem_test
```
