// Package transform compresses and decompresses bytes with the SDK's
// compressors, without the codec package (ADR 0014). Importing it registers
// three, which the standard library implements:
//
//	box, err := transform.Compress(transform.Gzip, nil, payload)
//	plain, err := transform.DecompressBounded(transform.Gzip, nil, box, 1<<20)
//
// # Algorithms
//
// An [Algorithm] names a [Compressor] in a registry of its own — compression
// is a transform of bytes, never a codec Format. [Gzip] is RFC 1952, [Flate]
// the raw DEFLATE stream of RFC 1951, and [Zlib] the RFC 1950 envelope HTTP
// misnames "deflate": three wire formats, not aliases of one another. A
// compressor another package registers — the module
// github.com/kitsunium/sdk/third-party/transform adds zstd and s2 — is reached
// by its Algorithm the same way once that package is imported; [Available]
// lists what is registered.
//
// Every function appends to dst, which may be nil, and src must not overlap
// dst's spare capacity: a scheme writes there while it still reads src.
//
// # Bounds
//
// [Decompress] is bounded by the scheme's own backstop — 256 MiB for the three
// registered here — so a bomb cannot exhaust memory, but it is the scheme's
// number, not the caller's. [DecompressBounded] takes the caller's ceiling: a
// scheme implementing [BoundedDecompressor] stops at it, so the ceiling bounds
// the work and not only the verdict; a scheme that does not is decompressed
// under its own backstop and judged afterwards. Either way no more than limit
// bytes are returned, and a stream that would exceed it is refused with
// [DecompressedTooLarge] rather than cut short — a truncated success would
// turn a bomb into a parsing bug further up.
//
// # Errors
//
// Match them with errs.HasCode, or the sentinels with errors.Is:
//
//	CodeUnknownCompressor       0.2.5.1   no compressor is registered under the Algorithm
//	CodeCompressedFrameInvalid  0.2.5.4   the codec package's compressed frame is malformed, or its bomb guard tripped
//	CodeDecompressedTooLarge    0.2.5.6   the plaintext would exceed DecompressBounded's ceiling
//	CodeGzipFailed              0.3.26.1  compress/gzip refused the stream, compressing or decompressing
//	CodeFlateFailed             0.3.26.2  compress/flate refused the stream
//	CodeZlibFailed              0.3.26.3  compress/zlib refused the stream
//
// A malformed stream is the scheme's own refusal, with the scheme's code:
// [GzipFailed], [FlateFailed] or [ZlibFailed] for the three registered here,
// the stdlib's own error kept in the chain beneath it.
package transform
