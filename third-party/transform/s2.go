// Package transform — the s2 scheme over github.com/klauspost/compress/s2, the
// same module the zstd scheme uses, so it costs no second dependency.
package transform

import (
	"github.com/klauspost/compress/s2"

	coretransform "github.com/kitsunium/sdk/internal/core/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// S2Algorithm is the canonical registry key for the s2 scheme.
//
// s2 is NOT registered under "snappy" and makes no Snappy-compatibility claim:
// the block encoder here emits s2 extensions a Snappy decoder does not read, so
// naming it "snappy" would promise interop the bytes do not honour. The same
// mistake HTTP made with "deflate", refused here before it can be made.
const S2Algorithm coretransform.Algorithm = "s2"

// S2Compressor is the concrete core/transform.Compressor for the s2 block
// format. Unlike the zstd scheme it holds no encoder or decoder — s2's block
// API is a pair of free functions — so the value is stateless apart from its
// ceiling and needs no Close.
type S2Compressor struct {
	// maxDecompressedBytes is the ceiling this compressor refuses past.
	maxDecompressedBytes int64
}

// S2 is the registered s2 singleton, built at DefaultMaxDecompressedBytes.
var S2 = coretransform.Register(mustS2())

// mustS2 builds the default s2 compressor for the package-level singleton. It
// panics on failure for the same reason mustZstd does: the only reachable
// failure is a wrong constant in this package.
func mustS2() *S2Compressor {
	//: the singleton always uses the package default ceiling.
	c, err := NewS2Compressor(DefaultMaxDecompressedBytes)
	//: an error here means this file's own constants are wrong.
	if err != nil {
		//: fail loudly at boot rather than register nothing.
		panic(err.Error())
	}
	//: hand the singleton to Register.
	return c
}

// NewS2Compressor builds an s2 compressor refusing to decompress more than
// maxDecompressedBytes of plaintext in one call. The argument is positional and
// mandatory; a non-positive value returns LimitMisconfigured and no compressor.
//
// s2 has no level knob here on purpose. Its reason for existing is throughput,
// and the levels that buy ratio (s2.EncodeBetter / EncodeBest) land in zstd's
// territory at zstd's cost — a caller who wants that trade should name zstd
// rather than get it from a scheme chosen for speed.
func NewS2Compressor(maxDecompressedBytes int64) (compressor *S2Compressor, err error) {
	//: refuse an unbounded or nonsensical ceiling before returning anything.
	if lerr := checkLimit(maxDecompressedBytes); lerr != nil {
		//: no compressor exists that would not bound its output.
		return nil, lerr
	}
	//: stateless apart from the ceiling.
	return &S2Compressor{maxDecompressedBytes: maxDecompressedBytes}, nil
}

// Algorithm implements core/transform.Compressor and returns the registry key.
func (*S2Compressor) Algorithm() coretransform.Algorithm {
	//: canonical identifier, frozen per scheme.
	return S2Algorithm
}

// Compress s2-encodes src and appends the result to dst. dst and src must not
// overlap.
//
// s2.Encode treats its first argument as SCRATCH, not as a prefix — it
// overwrites from index zero and returns a sub-slice of it. Handing it the
// caller's dst directly would silently destroy the bytes already there, so the
// encode targets the TAIL of a grown dst and the prefix is preserved by
// construction. TestCompressPreservesDstPrefix pins that, because the bug it
// prevents is invisible in every test that passes dst=nil.
//
// Encoding into the tail rather than into a fresh buffer is the one thing in
// this package chosen on a profile rather than on taste: the obvious version
// (encode to nil, then append) spends its time in runtime.memmove copying the
// result a second time. See BENCH.md §"Why s2 encodes into the tail".
//
// It never fails: s2.Encode has no error return, and an input too large for the
// block format is refused before the encode by MaxEncodedLen.
func (s *S2Compressor) Compress(dst, src []byte) (encoded []byte, err error) {
	//: worst-case encoded size; negative means the block format cannot address it.
	bound := s2.MaxEncodedLen(len(src))
	//: an unaddressable input is a data error, not a silent truncation.
	if bound < 0 {
		//: refuse with the s2 sentinel, leaving dst exactly as it came in.
		return dst, errs.Wrap(S2Failed, errs.WrapParams{},
			errs.Int("plain_bytes", len(src)))
	}
	//: remember where the caller's bytes end so the tail can be handed to s2.
	start := len(dst)
	//: extend dst by the worst case so s2 encodes in place instead of allocating.
	grown := append(dst, make([]byte, bound)...)
	//: s2 fills the tail and returns the sub-slice it actually used.
	out := s2.Encode(grown[start:], src)
	//: trim the grown buffer back to prefix + what s2 actually wrote.
	return grown[:start+len(out)], nil
}

// Decompress s2-decodes src and appends the result to dst, refusing any payload
// whose declared plaintext length exceeds the configured ceiling.
//
// The refusal happens BEFORE any decode and before any allocation: the s2 block
// format carries its decoded length in a varint header, so DecodedLen answers
// the bomb question by reading a handful of bytes. A refused payload costs the
// varint read and nothing else.
func (s *S2Compressor) Decompress(dst, src []byte) (decoded []byte, err error) {
	//: read the declared plaintext length straight out of the block header.
	want, lerr := s2.DecodedLen(src)
	//: a corrupt or absent header is a malformed stream, not a bomb.
	if lerr != nil {
		//: hand back dst untouched under the s2 sentinel.
		return dst, errs.Wrap(lerr, s2Wrap)
	}
	//: the header's own claim is enough to refuse — before allocating a byte.
	if int64(want) > s.maxDecompressedBytes {
		//: report the bomb refusal with the numbers an operator needs.
		return dst, errs.Wrap(DecompressionLimitExceeded, errs.WrapParams{},
			errs.String("algorithm", string(S2Algorithm)),
			errs.Int64("max_decompressed_bytes", s.maxDecompressedBytes),
			errs.Int("declared_bytes", want),
			errs.Int("compressed_bytes", len(src)))
	}
	//: the header already told us the exact size, so grow dst once and let s2
	//: decode straight into the tail — s2.Decode overwrites its dst as Encode
	//: does, so it gets the tail and never the caller's prefix.
	start := len(dst)
	//: extend by exactly the declared length; the bound above makes that safe.
	grown := append(dst, make([]byte, want)...)
	//: decode into the tail.
	plain, derr := s2.Decode(grown[start:], src)
	//: a payload that does not honour its own header is a malformed stream.
	if derr != nil {
		//: hand back dst at its original length under the s2 sentinel.
		return dst[:start], errs.Wrap(derr, s2Wrap)
	}
	//: trim back to prefix + what s2 actually wrote.
	return grown[:start+len(plain)], nil
}
