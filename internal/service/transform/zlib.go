// Package transform — the zlib Compressor over compress/zlib (RFC 1950): a
// two-byte header, a raw DEFLATE body, and an Adler-32 trailer. This is NOT the
// `flate` scheme: `flate` is the bare DEFLATE stream of RFC 1951, with no header
// and no checksum, so the two are not wire-compatible in either direction.
// HTTP's `Content-Encoding: deflate` (RFC 9110 §8.4.1) names the zlib envelope,
// so this scheme — not `flate` — is the one that interoperates with it. Shares
// the package with the gzip and flate schemes; all three self-register at
// import.
package transform

import (
	"bytes"
	"compress/zlib"

	coretransform "github.com/kitsunium/sdk/internal/core/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// ZlibCompressor is the zlib singleton, registered with core/transform at
// package load at the stdlib default level. Binding the registration result to a
// named var is idiomatic and keeps us clear of init().
var ZlibCompressor = coretransform.Register(zlibCompressor{level: zlib.DefaultCompression})

// zlibCompressor is the concrete Compressor for the zlib wire format. The level
// is fixed at construction and never mutated, so the value stays comparable
// (idempotent re-registration is a no-op) and safe for concurrent use — the same
// stateless-value shape the gzip and flate schemes use.
type zlibCompressor struct {
	level int
}

// NewZlibCompressor returns a zlib Compressor encoding at level, which may be
// zlib.DefaultCompression, zlib.HuffmanOnly, or any value from zlib.BestSpeed to
// zlib.BestCompression. Anything else — including zlib.NoCompression, which the
// stdlib accepts but which stores the payload verbatim — is clamped to
// zlib.DefaultCompression, so no configuration can hand back a compressor that
// silently does not compress (ADR 0031). The result is NOT registered; the
// registry entry is the default-level ZlibCompressor singleton.
func NewZlibCompressor(level int) coretransform.Compressor {
	//: clamp at construction so the returned Compressor always compresses.
	return zlibCompressor{level: usableZlibLevel(level)}
}

// usableZlibLevel maps level onto a level that actually compresses, returning
// zlib.DefaultCompression for anything that does not. Two distinct faults land
// here: a level outside [HuffmanOnly, BestCompression], which the stdlib writer
// rejects outright, and zlib.NoCompression, which the stdlib accepts and which
// produces exactly the inert compressor ADR 0031 forbids. Clamping rather than
// refusing is the right side of the ADR 0031 line because every accepted level
// round-trips the caller's bytes identically — the knob trades ratio against
// CPU, it is not the content of the request — and because the sibling gzip and
// flate schemes already hard-code DefaultCompression, which makes it the floor a
// reader accepts without being told the number.
func usableZlibLevel(level int) int {
	//: two distinct faults share one outcome — a level outside the stdlib's
	//: accepted band, which NewWriterLevel rejects outright, and NoCompression,
	//: which it accepts yet which stores the payload verbatim.
	if level < zlib.HuffmanOnly || level > zlib.BestCompression || level == zlib.NoCompression {
		//: fall back to the level the sibling gzip/flate schemes encode at.
		return zlib.DefaultCompression
	}
	//: DefaultCompression, HuffmanOnly and BestSpeed..BestCompression compress.
	return level
}

// Algorithm implements transform.Compressor and returns the canonical key.
func (zlibCompressor) Algorithm() coretransform.Algorithm {
	//: canonical identifier, frozen per scheme; no pkg/v1/codec frame algID is
	//: allocated for zlib yet, so MarshalCompressed cannot name it (CLAUDE.md).
	return "zlib"
}

// Compress zlib-encodes src at the receiver's level and appends the result to
// dst.
func (c zlibCompressor) Compress(dst, src []byte) (encoded []byte, err error) {
	//: encode into a buffer pre-seeded with dst so the result is caller-owned.
	buf := bytes.NewBuffer(dst)
	//: NewZlibCompressor already clamped level, so this call cannot reject it; an
	//: in-package struct literal can still reach the fault path (tested), and it
	//: reaches it through the unpooled branch takeZlibWriter keeps for it.
	w, nerr := takeZlibWriter(buf, c.level)
	//: NewWriterLevel only errors on an out-of-range level — guarded, but surfaced.
	if nerr != nil {
		//: wrap the construction error under the zlib sentinel.
		return dst, errs.Wrap(nerr, zlibWrap)
	}
	//: return the encoder to its level's pool on every exit.
	defer releaseZlibWriter(w, c.level)
	//: a Write fault is rare (buffer-backed) but must still be surfaced.
	if _, werr := w.Write(src); werr != nil {
		//: wrap the stdlib error under the zlib sentinel.
		return dst, errs.Wrap(werr, zlibWrap)
	}
	//: Close flushes the Adler-32 trailer; its error is the authoritative result.
	if cerr := w.Close(); cerr != nil {
		//: wrap the flush failure under the zlib sentinel.
		return dst, errs.Wrap(cerr, zlibWrap)
	}
	//: buf owns the appended bytes; hand them back to the caller.
	return buf.Bytes(), nil
}

// Decompress zlib-decodes src and appends the result to dst, bounded by the
// production ceiling (maxDecompressedBytes, see bounded.go) so a bomb cannot
// drive an OOM here. It delegates to zlibDecompress, the cap-parameterised core a
// white-box test drives with a lowered cap to exercise the overflow backstop.
// Decoding ignores the receiver's level — the level rides in the stream header.
func (zlibCompressor) Decompress(dst, src []byte) (decoded []byte, err error) {
	//: production always uses the full 256 MiB ceiling.
	return zlibDecompress(dst, src, maxDecompressedBytes)
}

// zlibDecompress zlib-decodes src and appends the result to dst, refusing to
// materialise more than max plaintext bytes. An over-cap stream returns the
// ZlibFailed sentinel rather than an OOM; a bad header, a corrupt body, or a
// failed Adler-32 check returns a wrapped zlib error. max is an explicit
// parameter so the overflow backstop is testable without mutating shared state.
func zlibDecompress(dst, src []byte, max int64) (decoded []byte, err error) {
	//: a recycled zlib reader validates the 2-byte header up-front, exactly as a
	//: fresh one does; a bad header fails here either way.
	box, rerr := takeZlibReader(bytes.NewReader(src))
	//: malformed header — surface the failure via the zlib sentinel.
	if rerr != nil {
		//: wrap the header error under the zlib sentinel.
		return dst, errs.Wrap(rerr, zlibWrap)
	}
	//: return the decoder on every exit below.
	defer releaseZlibReader(box)
	//: drain the reader through the shared bounded helper at the given cap.
	plain, overflow, derr := readAllBounded(box.rc, max)
	//: fold a Close fault into the result so the reader error is never dropped.
	if cerr := box.rc.Close(); cerr != nil && derr == nil {
		//: a clean drain followed by a Close fault still fails decompression.
		derr = cerr
	}
	//: a read/close fault (corrupt body, bad checksum, truncation) is a failure.
	if derr != nil {
		//: wrap the body error under the zlib sentinel.
		return dst, errs.Wrap(derr, zlibWrap)
	}
	//: an over-cap stream is treated as a failure, never an OOM.
	if overflow {
		//: ZlibFailed is the sentinel for any zlib-direction failure.
		return dst, ZlibFailed
	}
	//: append the bounded plaintext onto the caller's dst.
	return append(dst, plain...), nil
}
