package transform

import (
	"bytes"
	"compress/gzip"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// GzipCompressor is the gzip singleton, registered with core/data/transform at
// package load. Binding the registration result to a named var is idiomatic and
// keeps us clear of init().
var GzipCompressor = coretransform.Register(gzipCompressor{})

// gzipCompressor is the concrete Compressor for the gzip wire format. The level
// is fixed at construction and never mutated, so the value stays comparable
// (idempotent re-registration is a no-op) and safe for concurrent use.
type gzipCompressor struct {
	// level is the compress/gzip level, already clamped by usableLevel. Its
	// zero value means DefaultCompression: NoCompression is the one level the
	// clamp never lets through, so 0 is free to name the default, and the
	// registered singleton is the zero value it has always been.
	level int
}

// NewGzipCompressor returns a gzip Compressor encoding at level, which may be
// gzip.DefaultCompression, gzip.HuffmanOnly, or any value from gzip.BestSpeed to
// gzip.BestCompression. Anything else — including gzip.NoCompression, which the
// stdlib accepts but which stores the payload verbatim — is clamped to
// gzip.DefaultCompression, so no configuration can hand back a compressor that
// silently does not compress (ADR 0031). The result is NOT registered; the
// registry entry is the default-level GzipCompressor singleton, which
// NewGzipCompressor(gzip.DefaultCompression) equals.
func NewGzipCompressor(level int) coretransform.Compressor {
	//: clamp at construction so the returned Compressor always compresses.
	return gzipCompressor{level: storedLevel(usableLevel(level))}
}

// storedLevel maps a clamped level onto the field's representation: the
// default is stored as the zero value, so the singleton and a default-level
// compressor built by a constructor are one value.
func storedLevel(level int) int {
	//: DefaultCompression is the zero value.
	if level == gzip.DefaultCompression {
		return 0
	}
	//: every other clamped level is stored as is.
	return level
}

// writeLevel is the level the stdlib writer is built at: the stored level,
// the zero value read as DefaultCompression.
func writeLevel(stored int) int {
	//: the zero value is the default, never NoCompression.
	if stored == 0 {
		return gzip.DefaultCompression
	}
	//: any other stored level was clamped at construction.
	return stored
}

// Algorithm implements transform.Compressor and returns the canonical key.
func (gzipCompressor) Algorithm() coretransform.Algorithm {
	//: canonical identifier, frozen per scheme (algID 0x01 at the frame layer).
	return "gzip"
}

// Compress gzip-encodes src at the receiver's level and appends the result to
// dst.
func (c gzipCompressor) Compress(dst, src []byte) (encoded []byte, err error) {
	//: encode into a buffer pre-seeded with dst so the result is caller-owned.
	buf := bytes.NewBuffer(dst)
	level := writeLevel(c.level)
	//: a recycled writer streams compressed bytes into buf; see pool.go for why
	//: constructing one per call was 99 % of this path's allocated bytes.
	w, nerr := takeGzipWriter(buf, level)
	//: NewWriterLevel only errors on an out-of-range level — the constructor's
	//: clamp makes it unreachable, an in-package struct literal does not.
	if nerr != nil {
		//: wrap the construction error under the gzip sentinel.
		return dst, errs.Wrap(nerr, gzipWrap)
	}
	//: return the encoder to its level's pool on every exit.
	defer releaseGzipWriter(w, level)
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

// DecompressBounded implements core/data/transform.BoundedDecompressor: it decodes
// src under the CALLER's ceiling instead of this layer's backstop, so a caller
// enforcing a tighter limit stops the work at its own bound rather than
// materialising the full backstop and judging the result afterwards.
//
// max is honoured at every value: zero admits an empty stream and refuses every
// other, and a negative ceiling is clamped to zero. An over-cap stream returns
// coretransform.DecompressedTooLarge, which states only that more than max
// bytes were produced.
func (c gzipCompressor) DecompressBounded(dst, src []byte, max int64) (decoded []byte, err error) {
	//: a ceiling of zero is a legitimate one — it admits an empty stream and
	//: nothing else — so it flows through the core like any other. This used to
	//: fall back to Decompress on max <= 0, justified by "a non-positive
	//: ceiling would admit everything through LimitReader's max+1". That
	//: rationale is measurably false: gzipDecompressCore(src, 0) reports
	//: tooLarge for a 4096-byte payload and NOT tooLarge for an empty stream.
	//: What the fallback actually did was LOOSEN the caller's 0-byte ceiling to
	//: this layer's 256 MiB backstop and hand back 4096 bytes of plaintext.
	//: A negative ceiling holds nothing either, so it is clamped rather than
	//: given a third behaviour; the bound then stays monotone in max.
	ceiling := max
	//: below zero there is no buffer a caller could be asking for.
	if ceiling < 0 {
		//: the smallest ceiling that means anything.
		ceiling = 0
	}
	//: the shared core, so the ceiling bounds the WORK and not just the verdict.
	out, tooLarge, derr := gzipDecompressCore(dst, src, ceiling)
	//: a malformed stream is the scheme's business, surfaced verbatim.
	if derr != nil {
		//: already wrapped by the core.
		return out, derr
	}
	//: a stream that produced more plaintext than the caller agreed to hold.
	if tooLarge {
		//: distinct from GzipFailed: nothing was found malformed. Nothing was
		//: found well-formed either — the decode stopped at the ceiling, short
		//: of the trailer — which is precisely why this is a size fact and not
		//: a verdict on the stream.
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
		return dst, coretransform.GzipFailed
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
	box, rerr := takeGzipReader(src)
	//: malformed header — surface the failure via the gzip sentinel.
	if rerr != nil {
		//: wrap the header error under the gzip sentinel.
		return dst, false, errs.Wrap(rerr, gzipWrap)
	}
	//: return the decoder on every exit below.
	defer releaseGzipReader(box)
	//: drain the reader straight onto dst through the shared bounded helper.
	out, overflow, derr := appendAllBounded(dst, box.rc, max)
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
	//: the bounded plaintext, already on the caller's dst.
	return out, false, nil
}
