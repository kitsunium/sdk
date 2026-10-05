// Package transform — shared decompression bound for the stdlib gzip, flate and
// zlib schemes. Each Decompress reads through an io.LimitReader so a malformed or
// hostile stream cannot drive an unbounded allocation at this layer. The full
// self-describing decompression-bomb guard (max output size + max expansion
// ratio keyed to the frame header) lives in the pkg/v1/data/codec frame layer (a
// later commit, CodeCompressedFrameInvalid); this is the conservative
// layer-local backstop ADR 0014 D1 asks for.
//
// Package transform — the flate (raw DEFLATE) Compressor over compress/flate.
// Shares the package with the gzip scheme; both self-register at import.
//
// Package transform wraps the stdlib compress/gzip and compress/flate codecs as
// core/data/transform.Compressor implementations. Blank-importing this package is
// enough to make "gzip" and "flate" resolvable via the core/data/transform registry.
//
// Package transform — the recycled stdlib codecs behind Compress and
// Decompress. Neither direction's stdlib object is small: at the default level
// an encoder carries two hash tables and a 320 KiB history window, and a
// decoder carries a 32 KiB dictionary window. Constructing one per call put
// compress/flate at 99 % of the bytes a 256-byte gzip allocated and
// runtime.memclrNoHeapPointers at 18.7 % of its CPU — zeroing tables the call
// was about to overwrite. Recycling moves that off the per-call path; the
// before/after medians are in BENCH.md.
//
// A pooled object is reachable until the next take on the same P or the next
// GC, whichever comes first, and it is large. That is why the two directions
// unbind differently, and the line is what the SDK created versus what the
// caller lent us: an encoder is rebound to io.Discard before it goes back,
// because it would otherwise pin the output buffer this package built and
// handed to the caller as exclusively theirs; a decoder is not rebound,
// because what it holds is the caller's own compressed input, which the caller
// passed in and still has — and because every decoder Reset re-reads a header
// and so returns an error that an unbind would have to discard, while every
// encoder Reset returns nothing at all.
//
// Every take checks the pooled decoder rather than trusting it, and the check
// is in two parts because one of them cannot do the job alone: a type assertion
// establishes the Reset CAPABILITY, and a tag on the box establishes the
// SCHEME. readerScheme carries the measurement that forces the split — the
// flate and zlib Resetter interfaces declare the identical method, so each
// pool's assertion accepts the other pool's decoder and detects nothing.
//
// A box holding another scheme's decoder is a corrupted pool, which is this
// package's own defect and not the caller's stream: nobody who called
// Decompress could have caused it and nobody could act on being told. So it is
// never an error. It fails the same condition an empty box fails, takes the
// same constructor path, and the fresh decoder overwrites the foreign one — so
// the fault costs exactly the allocation the pool exists to avoid, once, and
// does not outlive the take that found it.
//
// Package transform — the WrapParams the three schemes attach to a stdlib
// cause. Each mirrors its sentinel — GzipFailed, FlateFailed, ZlibFailed,
// declared with their codes in internal/core/data/transform (ADR 0160) — so a
// wrapped compress/* failure carries the same Code/Reason/Public on the wire as
// the bare sentinel.
//
// Package transform — the zlib Compressor over compress/zlib (RFC 1950): a
// two-byte header, a raw DEFLATE body, and an Adler-32 trailer. This is NOT the
// `flate` scheme: `flate` is the bare DEFLATE stream of RFC 1951, with no header
// and no checksum, so the two are not wire-compatible in either direction.
// HTTP's `Content-Encoding: deflate` (RFC 9110 §8.4.1) names the zlib envelope,
// so this scheme — not `flate` — is the one that interoperates with it. Shares
// the package with the gzip and flate schemes; all three self-register at
// import.
package transform
