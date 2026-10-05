package transform

import (
	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"

	// The stdlib schemes register themselves — gzip, flate and zlib — as Go
	// initialises their package.
	_ "github.com/kitsunium/sdk/internal/service/data/transform"
)

// The Algorithms registered by importing this package.
const (
	// Gzip is RFC 1952: a DEFLATE stream in a gzip envelope with a CRC-32.
	Gzip Algorithm = "gzip"
	// Flate is the raw DEFLATE stream of RFC 1951, with no envelope.
	Flate Algorithm = "flate"
	// Zlib is the RFC 1950 envelope with an Adler-32 — what HTTP calls
	// "deflate".
	Zlib Algorithm = "zlib"
)

// Compress compresses src with the scheme registered under algo and appends
// the result to dst. An unregistered algo is refused with UnknownCompressor
// and dst comes back as it was.
func Compress(algo Algorithm, dst, src []byte) (encoded []byte, err error) {
	c, ok := coretransform.Lookup(algo)
	//: no scheme under that name: a missing import, named as such.
	if !ok {
		//: the sentinel itself, and dst untouched.
		return dst, coretransform.UnknownCompressor
	}
	//: the scheme wraps its own failure (origin wins).
	return c.Compress(dst, src)
}

// Decompress decompresses src with the scheme registered under algo and
// appends the result to dst, bounded by the scheme's own backstop. To bound
// it by a number of your own, call DecompressBounded. An unregistered algo is
// refused with UnknownCompressor and dst comes back as it was.
func Decompress(algo Algorithm, dst, src []byte) (decoded []byte, err error) {
	c, ok := coretransform.Lookup(algo)
	//: no scheme under that name: a missing import, named as such.
	if !ok {
		//: the sentinel itself, and dst untouched.
		return dst, coretransform.UnknownCompressor
	}
	//: the scheme's backstop applies; a malformed stream is its refusal.
	return c.Decompress(dst, src)
}

// DecompressBounded decompresses src with the scheme registered under algo
// and appends the result to dst, producing at most limit bytes of plaintext:
// a stream that would produce more is refused with DecompressedTooLarge and
// dst comes back as it was. A limit of zero admits an empty stream and nothing
// else, and a negative limit is read as zero rather than given a meaning of
// its own.
//
// A scheme implementing BoundedDecompressor stops at limit, so the work is
// bounded too. A scheme that does not — a consumer's own, or one of the
// third-party module's — is decompressed under its own backstop and judged
// afterwards: declining to tighten the work can never change the verdict,
// only the bytes touched reaching it.
func DecompressBounded(algo Algorithm, dst, src []byte, limit int64) (decoded []byte, err error) {
	c, ok := coretransform.Lookup(algo)
	//: no scheme under that name: a missing import, named as such.
	if !ok {
		//: the sentinel itself, and dst untouched.
		return dst, coretransform.UnknownCompressor
	}
	//: a scheme that accepts a ceiling stops at the caller's.
	if bounded, isBounded := c.(coretransform.BoundedDecompressor); isBounded {
		//: the ceiling bounds the work, not only the verdict.
		return bounded.DecompressBounded(dst, src, limit)
	}
	//: any other scheme: its own backstop bounds the work, the ceiling the result.
	return judgeAfter(c, dst, src, limit)
}

// judgeAfter decompresses with a scheme that cannot be told a ceiling, under
// its own backstop, and refuses a result past limit.
func judgeAfter(c Compressor, dst, src []byte, limit int64) (decoded []byte, err error) {
	//: below zero there is no buffer a caller could be asking for, so zero is
	//: the smallest ceiling that means anything.
	ceiling := max(limit, 0)
	decoded, err = c.Decompress(dst, src)
	//: a malformed stream is the scheme's refusal, surfaced as it is.
	if err != nil {
		//: the scheme's own error.
		return decoded, err
	}
	//: more plaintext than the caller agreed to hold.
	if int64(len(decoded)-len(dst)) > ceiling {
		//: a size fact, not a verdict on the stream; dst as it was.
		return dst, coretransform.DecompressedTooLarge
	}
	//: within the caller's ceiling.
	return decoded, nil
}
