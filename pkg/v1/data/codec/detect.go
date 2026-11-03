package codec

import (
	"bytes"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
)

// The names DetectCompression answers, as the transform registry spells them.
const (
	// compressionGzip is gzip, RFC 1952.
	compressionGzip CompressAlgorithm = "gzip"
	// compressionZlib is zlib, RFC 1950.
	compressionZlib CompressAlgorithm = "zlib"
	// compressionZstd is Zstandard, RFC 8878.
	compressionZstd CompressAlgorithm = "zstd"
	// compressionS2 is S2, which also reads Snappy's framed stream.
	compressionS2 CompressAlgorithm = "s2"
)

// The zlib header DetectCompression accepts: DEFLATE with a 32 KiB window —
// the CMF every common encoder, compress/zlib included, writes — no preset
// dictionary, and the check bits RFC 1950 §2.2 defines.
const (
	// zlibCMF is the compression method and window byte.
	zlibCMF byte = 0x78
	// zlibFDICT is the preset-dictionary flag in FLG.
	zlibFDICT byte = 0x20
	// zlibCheckDivisor divides CMF*256+FLG exactly.
	zlibCheckDivisor uint16 = 31
	// zlibHeaderLen is the two header bytes.
	zlibHeaderLen int = 2
)

// The magic numbers DetectCompression reads.
var (
	// gzipMagic is gzip's ID1, ID2 and its only compression method, DEFLATE.
	gzipMagic = []byte{0x1f, 0x8b, 0x08}
	// zstdMagic is a Zstandard frame's magic number, little-endian.
	zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}
	// s2StreamMagic is S2's framed stream identifier chunk.
	s2StreamMagic = []byte("\xff\x06\x00\x00S2sTwO")
	// snappyStreamMagic is Snappy's framed stream identifier chunk, which S2
	// reads.
	snappyStreamMagic = []byte("\xff\x06\x00\x00sNaPpY")
)

// Detect returns the Format the first bytes of an input are written in,
// without a MIME type or a file extension (#909): JSON, NDJSON (two records
// or more), XML, PEM or BSON, recognised by their head. It reads at most 512
// bytes of prefix, as http.DetectContentType does, answers (Format(""),
// false) when no format — or more than one — claims them, and allocates
// nothing. A frame MarshalCompressed wrote is no Format — UnmarshalCompressed
// reads it — so Detect never claims one; compressed bytes are
// DetectCompression's.
func Detect(prefix []byte) (f Format, ok bool) {
	//: this package's own frame, whose payload a sniffer could misread.
	if isFrame(prefix) {
		//: not a Format.
		return "", false
	}
	//: the codecs' sniffers decide.
	return corecodec.Detect(prefix)
}

// DetectCompression returns the compression the first bytes of an input open
// with, by its magic number (#909): "gzip" (1f 8b 08), "zlib" (an RFC 1950
// header whose check bits hold), "zstd" (28 b5 2f fd) or "s2" (the framed
// stream identifier of S2 or Snappy). It answers ("", false) for anything
// else — raw DEFLATE ("flate") and S2's block form have no magic, and a
// frame MarshalCompressed wrote is UnmarshalCompressed's — and allocates
// nothing. zstd and s2 are named, not decompressed, here: they live in
// third-party/transform (ADR 0066). Decompress, then Detect what is inside.
func DetectCompression(prefix []byte) (algo CompressAlgorithm, ok bool) {
	switch {
	//: gzip.
	case bytes.HasPrefix(prefix, gzipMagic):
		return compressionGzip, true
	//: a Zstandard frame.
	case bytes.HasPrefix(prefix, zstdMagic):
		return compressionZstd, true
	//: an S2 or Snappy framed stream.
	case bytes.HasPrefix(prefix, s2StreamMagic), bytes.HasPrefix(prefix, snappyStreamMagic):
		return compressionS2, true
	//: a zlib header.
	case zlibHeader(prefix):
		return compressionZlib, true
	//: no magic this function knows.
	default:
		return "", false
	}
}

// zlibHeader reports whether p opens with the zlib header DetectCompression
// accepts: CMF 0x78, no preset dictionary, and CMF*256+FLG a multiple of 31.
func zlibHeader(p []byte) bool {
	//: the two header bytes.
	if len(p) < zlibHeaderLen || p[0] != zlibCMF || p[1]&zlibFDICT != 0 {
		//: not one.
		return false
	}
	//: the check bits.
	return (uint16(p[0])<<8|uint16(p[1]))%zlibCheckDivisor == 0
}

// isFrame reports whether p opens with this package's compressed frame: the
// magic, the version and an algorithm id the frame defines.
func isFrame(p []byte) bool {
	//: the fixed header's first three bytes.
	return len(p) >= frameHeaderLen && p[0] == frameMagic && p[1] == frameVersion &&
		(p[algIDIndex] == algGzip || p[algIDIndex] == algFlate)
}
