// Package transform wraps the stdlib compress/gzip and compress/flate codecs as
// core/transform.Compressor implementations. Blank-importing this package is
// enough to make "gzip" and "flate" resolvable via the core/transform registry.
package transform

import (
	"bytes"
	"compress/gzip"

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
	//: stdlib gzip writer streams compressed bytes into buf.
	w := gzip.NewWriter(buf)
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

// Decompress gzip-decodes src and appends the result to dst, bounded by
// maxDecompressedBytes (see bounded.go) so a bomb cannot drive an OOM here.
func (gzipCompressor) Decompress(dst, src []byte) (decoded []byte, err error) {
	//: a gzip reader validates the header up-front; a bad header fails here.
	r, rerr := gzip.NewReader(bytes.NewReader(src))
	//: malformed header — surface the failure via the gzip sentinel.
	if rerr != nil {
		//: wrap the header error under the gzip sentinel.
		return dst, errs.Wrap(rerr, gzipWrap)
	}
	//: drain the reader through the shared bounded helper.
	plain, overflow, derr := readAllBounded(r)
	//: fold a Close fault into the result so the reader error is never dropped.
	if cerr := r.Close(); cerr != nil && derr == nil {
		//: a clean drain followed by a Close fault still fails decompression.
		derr = cerr
	}
	//: a read/close fault (corrupt body / truncated stream) is a failure.
	if derr != nil {
		//: wrap the body error under the gzip sentinel.
		return dst, errs.Wrap(derr, gzipWrap)
	}
	//: an over-cap stream is treated as a failure, never an OOM.
	if overflow {
		//: GzipFailed is the sentinel for any gzip-direction failure.
		return dst, GzipFailed
	}
	//: append the bounded plaintext onto the caller's dst.
	return append(dst, plain...), nil
}
