// Package transform wraps the stdlib compress/gzip and compress/flate codecs as
// core/transform.Compressor implementations. Blank-importing this package is
// enough to make "gzip" and "flate" resolvable via the core/transform registry.
package transform

import (
	"bytes"

	coretransform "github.com/kitsunium/sdk/internal/core/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// GzipCompressor is the gzip singleton, registered with core/transform at
// package load. Binding the registration result to a named var is idiomatic and
// keeps us clear of init().
var GzipCompressor = coretransform.Register(gzipCompressor{})

// gzipCompressor is the concrete Compressor for the gzip wire format. It is a
// stateless value type so idempotent re-registration is a no-op.
type gzipCompressor struct{}

// Algorithm implements transform.Compressor and returns the canonical key.
func (gzipCompressor) Algorithm() coretransform.Algorithm {
	//: canonical identifier, frozen per scheme (algID 0x01 at the frame layer).
	return "gzip"
}

// Compress gzip-encodes src and appends the result to dst.
func (gzipCompressor) Compress(dst, src []byte) (encoded []byte, err error) {
	//: encode into a buffer pre-seeded with dst so the result is caller-owned.
	buf := bytes.NewBuffer(dst)
	//: a recycled writer streams compressed bytes into buf; see pool.go for why
	//: constructing one per call was 99 % of this path's allocated bytes.
	w := takeGzipWriter(buf)
	//: return the encoder on every exit, including the two failure paths.
	defer releaseGzipWriter(w)
	//: a Write fault is rare (buffer-backed) but must still be surfaced.
	if _, werr := w.Write(src); werr != nil {
		//: wrap the stdlib error under the gzip sentinel.
		return dst, errs.Wrap(werr, gzipWrap)
	}
	//: Close flushes the trailer; its error is the authoritative encode result.
	if cerr := w.Close(); cerr != nil {
		//: wrap the flush failure under the gzip sentinel.
		return dst, errs.Wrap(cerr, gzipWrap)
	}
	//: buf owns the appended bytes; hand them back to the caller.
	return buf.Bytes(), nil
}

// DecompressBounded implements core/transform.BoundedDecompressor: it decodes
// src under the CALLER's ceiling instead of this layer's backstop, so a caller
// enforcing a tighter limit stops the work at its own bound rather than
// materialising the full backstop and judging the result afterwards.
func (c gzipCompressor) DecompressBounded(dst, src []byte, max int64) (decoded []byte, err error) {
	//: a non-positive ceiling would admit everything through LimitReader's
	//: max+1; fall back to this layer's own backstop rather than silently
	//: disabling the bound the caller asked for.
	if max <= 0 {
		//: the documented layer-local ceiling.
		return c.Decompress(dst, src)
	}
	//: the shared core, so the ceiling bounds the WORK and not just the verdict.
	out, tooLarge, derr := gzipDecompressCore(dst, src, max)
	//: a malformed stream is the scheme's business, surfaced verbatim.
	if derr != nil {
		//: already wrapped by the core.
		return out, derr
	}
	//: a well-formed stream larger than the caller agreed to hold.
	if tooLarge {
		//: distinct from GzipFailed: nothing was malformed.
		return dst, coretransform.DecompressedTooLarge
	}
	//: within the caller's ceiling.
	return out, nil
}

// Decompress gzip-decodes src and appends the result to dst, bounded by the
// production ceiling (maxDecompressedBytes, see bounded.go) so a bomb cannot
// drive an OOM here. It delegates to gzipDecompress, the cap-parameterised core
// a white-box test drives with a lowered cap to exercise the overflow backstop.
func (gzipCompressor) Decompress(dst, src []byte) (decoded []byte, err error) {
	//: production always uses the full 256 MiB ceiling.
	return gzipDecompress(dst, src, maxDecompressedBytes)
}

// gzipDecompress gzip-decodes src and appends the result to dst, refusing to
// materialise more than max plaintext bytes. An over-cap stream returns the
// GzipFailed sentinel rather than an OOM; a bad header or corrupt body returns a
// wrapped gzip error. max is an explicit parameter so the overflow backstop is
// testable without mutating shared state.
func gzipDecompress(dst, src []byte, max int64) (decoded []byte, err error) {
	//: the layer backstop collapses "too big" into the scheme sentinel, which
	//: is the contract this layer has always had and its tests still assert.
	out, tooLarge, derr := gzipDecompressCore(dst, src, max)
	//: a malformed stream surfaces verbatim.
	if derr != nil {
		//: already wrapped by the core.
		return out, derr
	}
	//: over the layer's own ceiling — the scheme's failure sentinel.
	if tooLarge {
		//: GzipFailed is the sentinel for any gzip-direction failure.
		return dst, GzipFailed
	}
	//: within bounds.
	return out, nil
}

// gzipDecompressCore is the shared body behind both the layer backstop and the
// caller-supplied ceiling. It reports an over-cap stream as tooLarge rather
// than choosing a sentinel, so each caller can name the fact in its own terms.
func gzipDecompressCore(dst, src []byte, max int64) (decoded []byte, tooLarge bool, err error) {
	//: a recycled gzip reader validates the header up-front, exactly as a fresh
	//: one does; a bad header fails here either way.
	box, rerr := takeGzipReader(bytes.NewReader(src))
	//: malformed header — surface the failure via the gzip sentinel.
	if rerr != nil {
		//: wrap the header error under the gzip sentinel.
		return dst, false, errs.Wrap(rerr, gzipWrap)
	}
	//: return the decoder on every exit below.
	defer releaseGzipReader(box)
	//: drain the reader through the shared bounded helper at the given cap.
	plain, overflow, derr := readAllBounded(box.rc, max)
	//: fold a Close fault into the result so the reader error is never dropped.
	if cerr := box.rc.Close(); cerr != nil && derr == nil {
		//: a clean drain followed by a Close fault still fails decompression.
		derr = cerr
	}
	//: a read/close fault (corrupt body / truncated stream) is a failure.
	if derr != nil {
		//: wrap the body error under the gzip sentinel.
		return dst, false, errs.Wrap(derr, gzipWrap)
	}
	//: an over-cap stream is reported as such; the CALLER picks the sentinel,
	//: because "too big" means different things to the layer that owns the
	//: backstop and to one that supplied a tighter ceiling of its own.
	if overflow {
		//: no plaintext escapes an over-cap stream.
		return dst, true, nil
	}
	//: append the bounded plaintext onto the caller's dst.
	return append(dst, plain...), false, nil
}
