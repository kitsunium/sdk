// Package transform — the flate (raw DEFLATE) Compressor over compress/flate.
// Shares the package with the gzip scheme; both self-register at import.
package transform

import (
	"bytes"
	"compress/flate"

	coretransform "github.com/kitsunium/sdk/internal/core/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// FlateCompressor is the flate (raw DEFLATE) singleton, registered with
// core/transform at package load. Binding the registration result to a named
// var keeps us clear of init().
var FlateCompressor = coretransform.Register(flateCompressor{})

// flateCompressor is the concrete Compressor for the raw DEFLATE wire format. It
// is a stateless value type so idempotent re-registration is a no-op.
type flateCompressor struct{}

// Algorithm implements transform.Compressor and returns the canonical key.
func (flateCompressor) Algorithm() coretransform.Algorithm {
	//: canonical identifier, frozen per scheme (algID 0x02 at the frame layer).
	return "flate"
}

// Compress DEFLATE-encodes src and appends the result to dst.
func (flateCompressor) Compress(dst, src []byte) (encoded []byte, err error) {
	//: encode into a buffer pre-seeded with dst so the result is caller-owned.
	buf := bytes.NewBuffer(dst)
	//: DefaultCompression matches the stdlib default the gzip writer uses too.
	w, nerr := flate.NewWriter(buf, flate.DefaultCompression)
	//: NewWriter only errors on an out-of-range level — guarded, but surfaced.
	if nerr != nil {
		//: wrap the construction error under the flate sentinel.
		return dst, errs.Wrap(nerr, flateWrap)
	}
	//: a Write fault is rare (buffer-backed) but must still be surfaced.
	if _, werr := w.Write(src); werr != nil {
		//: wrap the stdlib error under the flate sentinel.
		return dst, errs.Wrap(werr, flateWrap)
	}
	//: Close flushes the final block; its error is the authoritative result.
	if cerr := w.Close(); cerr != nil {
		//: wrap the flush failure under the flate sentinel.
		return dst, errs.Wrap(cerr, flateWrap)
	}
	//: buf owns the appended bytes; hand them back to the caller.
	return buf.Bytes(), nil
}

// Decompress DEFLATE-decodes src and appends the result to dst, bounded by
// maxDecompressedBytes (see bounded.go) so a bomb cannot drive an OOM here.
func (flateCompressor) Decompress(dst, src []byte) (decoded []byte, err error) {
	//: raw DEFLATE has no header to pre-validate; the reader fails on read.
	r := flate.NewReader(bytes.NewReader(src))
	//: drain the reader through the shared bounded helper.
	plain, overflow, derr := readAllBounded(r)
	//: fold a Close fault into the result so the reader error is never dropped.
	if cerr := r.Close(); cerr != nil && derr == nil {
		//: a clean drain followed by a Close fault still fails decompression.
		derr = cerr
	}
	//: a read/close fault (corrupt body / truncated stream) is a failure.
	if derr != nil {
		//: wrap the body error under the flate sentinel.
		return dst, errs.Wrap(derr, flateWrap)
	}
	//: an over-cap stream is treated as a failure, never an OOM.
	if overflow {
		//: FlateFailed is the sentinel for any flate-direction failure.
		return dst, FlateFailed
	}
	//: append the bounded plaintext onto the caller's dst.
	return append(dst, plain...), nil
}
