# ADR 0213 — a compression stream is read under the caller's ceiling

- **Status**: Accepted
- **Date**: 2026-11-03
- **Deciders**: SDK maintainers
- **Amends**: nothing published but a sibling port of `core/data/transform`
  (ADR 0039), `StreamingCompressor`, the code `0.2.5.7` `STREAMING_UNSUPPORTED`,
  and in `pkg/v1/data/transform` their aliases and the two functions
  `NewReader` and `NewWriter`.
- **Closes**: [ADR 0066](0066-third-party-compressors.md) §Why not, "Expose the
  streaming APIs. Deferred."
- **Related**: [ADR 0014](0014-sdk-transform-crypto-ports-config-topology.md)
  (the transform port and its registry), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md)
  (a published port grows by a sibling interface), [ADR 0031](0031-policy-zero-values-are-never-inert.md)
  (a ceiling is refused or honoured, never widened), ADR 0066 D4 and D5 (the
  vendor schemes' ceiling and bomb code), [kitsunium/sdk#892](https://github.com/kitsunium/sdk/issues/892)

## Context

`core/data/transform.Compressor` is a whole-buffer port: `Compress` and
`Decompress` take and return `[]byte`. Every scheme behind it streams
internally — `compress/gzip` and the klauspost decoders read an `io.Reader` —
and the SDK wraps its input in `bytes.NewReader` to call them. So an HTTP body,
a log shipment or a file cannot go through the SDK's compressors without being
held whole on both sides: the facade's own frame ceiling is 64 MiB, and a
64 MiB frame is 64 MiB of plaintext and its compressed form in memory at once.
A caller who must not hold it reaches for `compress/gzip` directly, and loses
the bound that comes with the SDK (`DecompressedTooLarge`, `0.2.5.6`).

ADR 0066 deferred the streaming APIs for two stated reasons: a streaming
compressor is a port change, not a scheme addition — so it is a sibling
interface under ADR 0039, not a widening of `Compressor` — and it needs its own
bomb story, "because a stream has no declared output length to check".

## Decision

1. **A sibling port, detected by assertion.** `StreamingCompressor` embeds
   `Compressor` and adds `NewReader(r io.Reader, max int64) (io.ReadCloser,
   error)` and `NewWriter(w io.Writer) (io.WriteCloser, error)`. `Compressor`
   does not change: a consumer's scheme without a stream form keeps compiling
   and keeps serving whole buffers. The stdlib gzip, flate and zlib, and the
   vendor zstd and s2, implement it.

2. **The bomb story: the ceiling is the stream's, and it is counted.** A stream
   declares no length, so the bound cannot be read from a header; it is counted
   as the bytes go out. `NewReader` hands over at most `max` bytes. Once they
   are out, it asks the decoder for one more: a byte means the stream wanted
   more than `max`, and the read fails with `DecompressedTooLarge` — after `max`
   bytes, never instead of them, and sticky — while the end means the stream
   fitted, and the read returns `io.EOF`. This is `DecompressBounded`'s
   detector byte (ADR 0014, `appendAllBounded`) in stream form, and the same
   ceiling rules hold: zero admits an empty stream, a negative ceiling is zero,
   never "unlimited". `max` bounds what the CALLER receives; what the decoder
   holds is its window — 32 KiB for DEFLATE, the compressor's own ceiling for
   zstd (`WithDecoderMaxMemory` caps the window a frame may declare, so a
   larger one is ADR 0066 D5's `DECOMPRESSION_LIMIT_EXCEEDED` at its header),
   one 4 MiB block for s2. Measured: a gzip stream ten times its 4 MiB ceiling
   stops at 4 MiB after reading 12 KiB of its 100 KiB source, having allocated
   55 KiB (`TestAStreamStopsAtItsLimit`).

3. **The end is verified, not assumed.** `io.EOF` comes only once the stream's
   end has been read and, where the format has one, its checksum verified —
   gzip's CRC-32 and size, zlib's Adler-32, zstd's frame checksums, s2's chunk
   CRCs. A truncated stream is the scheme's own refusal (`GZIP_FAILED`,
   `ZSTD_FAILED`…), never a short success.

4. **A writer's stream ends at Close.** `NewWriter` compresses into `w`;
   `Close` writes the end — the trailer, where the format has one — and does
   not close `w`. A stream not closed is incomplete, and reads as truncated.

5. **Pooled, and released once.** The stream codecs come from the pools the
   whole-buffer path already keeps (the stdlib decoders and the per-level
   writers; for zstd one pool of each per compressor, built with its options
   and dictionary; one shared pool of each for s2) and go back on `Close`. A
   second `Close` is a no-op, and a read or a write after `Close` fails with the
   scheme's code over `io.ErrClosedPipe`, so a caller's mistake cannot reach a
   codec someone else now holds. Every codec runs in the caller's goroutine:
   zstd and s2 are built with a concurrency of one, so a stream spawns no
   goroutine.

6. **One wire form per scheme, said where it differs.** For gzip, flate, zlib
   and zstd, `NewWriter` writes exactly the bytes `Compress` writes (asserted
   for the stdlib three by the conformance table, byte for byte), and
   `NewReader` reads what `Compress` writes. s2 cannot: its block format
   declares its decoded length in a header, which a stream does not know, so its
   stream form is the s2 framing format — chunked, CRC-checked — which
   `Decompress` refuses and `Compress` does not write. Both directions are
   refused with `S2_FAILED`, never misread (`TestTheStreamFormsAgainstTheBlockForms`).

7. **The facade.** `pkg/v1/data/transform.NewReader(algo, r, limit)` and
   `NewWriter(algo, w)` resolve the Algorithm as `Compress` does; a registered
   scheme with no stream form is `STREAMING_UNSUPPORTED` (`0.2.5.7`), distinct
   from `UNKNOWN_COMPRESSOR` because the scheme exists and serves whole buffers.
   The port and the code are re-exported with it.

## Consequences

- A body, a file or a shipment goes through the SDK's compressors with the
  SDK's bound and without being held whole. The stream rows of both BENCH.md
  files put the stream within a few percent of the block form, and often
  ahead of it — the block form materialises the plaintext, the stream does not:
  gzip 4 MiB JSON decompresses in 19.4 ms against 24.5 ms, and allocates 6.8 KiB
  against 7.0 MiB. One exception is measured and kept: an s2 stream verifies a
  CRC-32C per chunk that the block format does not carry, which costs 10 % on
  JSON and 78 % on incompressible bytes, where decoding is a copy and the CRC is
  the work.
- A pooled decoder keeps a reference to the last caller's reader until its next
  use or the next collections, as the whole-buffer decoders keep the last
  input; a pooled encoder is unbound (`Reset(io.Discard)`) before it goes back.
- `pkg/v1/data/codec`'s compressed frame is untouched: it still holds a frame
  whole, under its own 64 MiB ceiling.

## Breaking changes

None. `Compressor` and `BoundedDecompressor` are unchanged; the port, the code,
the aliases and the two functions are additions.

## Alternatives considered

- **Widen `Compressor` with the two methods.** Rejected by ADR 0039: every
  consumer scheme would stop compiling.
- **`NewReader` without a ceiling, the caller wrapping it in `io.LimitReader`.**
  Rejected: a limit reader truncates silently, which turns a bomb into a
  parsing bug further up — the reason `DecompressBounded` refuses rather than
  truncates. The ceiling is a positional argument so it cannot be forgotten.
- **Refuse a stream at the decoder's window instead of counting output.** The
  window bounds memory, not output: a stream with a 32 KiB window can expand
  without limit. Both are kept: the window by the decoder, the output by the
  count.
- **A materialising fallback for schemes without a stream form** (read all,
  `DecompressBounded`, hand back a `bytes.Reader`). Rejected: it would hide,
  behind a stream's signature, exactly the whole-buffer cost a caller asking for
  a stream is avoiding; `STREAMING_UNSUPPORTED` says so instead.
- **Register s2's framing format under a name of its own** (`"s2-stream"`).
  Rejected for now: a stream form is a property of a scheme, not a scheme, and
  no consumer of the registry dispatches on it. The difference is documented on
  the methods and tested both ways.

## References

- `internal/core/data/transform/gen_ports.go` (`StreamingCompressor`, from
  `design/data/transform.yaml`), `internal/service/data/transform/stream.go`,
  `third-party/transform/stream.go`, `pkg/v1/data/transform/transform.go`
- `internal/service/data/transform/conformance_external_test.go` (the stream
  rows over every registered engine), `third-party/transform/conformance_external_test.go`
  (the same stream rows over zstd and s2), `third-party/transform/stream_external_test.go`,
  `pkg/v1/data/transform` `TestAStreamStopsAtItsLimit`
- `internal/service/data/transform/BENCH.md` §14, `third-party/transform/BENCH.md`
  §Streams
