package transform_test

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"errors"
	"io"
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svctransform "github.com/kitsunium/sdk/internal/service/data/transform"
)

// fuzzCeiling is the bound FuzzDecompressBounded decodes under: small, so a
// payload past it is common.
const fuzzCeiling int64 = 4096

// FuzzDecompressBounded is #897's transform target for the standard-library
// engines: whatever the input, DecompressBounded of gzip, flate and zlib never
// panics and never returns more than the ceiling; what it accepts, Decompress
// accepts with the same bytes, and Compress then DecompressBounded gives the
// input back.
func FuzzDecompressBounded(f *testing.F) {
	var engines []coretransform.BoundedDecompressor
	for _, c := range []coretransform.Compressor{
		svctransform.NewGzipCompressor(flate.DefaultCompression),
		svctransform.NewFlateCompressor(flate.DefaultCompression),
		svctransform.NewZlibCompressor(flate.DefaultCompression),
	} {
		bounded, ok := c.(coretransform.BoundedDecompressor)
		if !ok {
			f.Fatalf("%s is not a BoundedDecompressor", c.Algorithm())
		}
		engines = append(engines, bounded)
	}
	for _, e := range engines {
		small, _ := e.Compress(nil, []byte("kitsune kitsune kitsune"))
		big, _ := e.Compress(nil, bytes.Repeat([]byte{'a'}, 2*int(fuzzCeiling)))
		f.Add(small)
		f.Add(big)
	}
	f.Add([]byte{0x1f, 0x8b, 0x08, 0x00})
	f.Add([]byte{})
	//: the envelope seeds: an FDICT header, a header alone, a short trailer.
	f.Add([]byte{0x78, 0xbb, 0, 0, 0, 1, 3, 0, 0, 0, 0, 1})
	f.Add([]byte{0x78, 0x9c})
	f.Add([]byte{0x78, 0x9c, 3, 0, 0, 0, 0})
	//: a body that decodes past the ceiling and is truncated after it.
	f.Add([]byte("x\x9c\xec\xc0\x81\f\x00\x00\x00\xc30Z\xfbK\x9ccI;\x00\x00\x00\x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzZlibAgrees(t, engines[2], data)
		for _, e := range engines {
			out, err := e.DecompressBounded(nil, data, fuzzCeiling)
			//: never past the ceiling, refused or not.
			if int64(len(out)) > fuzzCeiling {
				t.Fatalf("%s: DecompressBounded returned %d bytes, ceiling %d", e.Algorithm(), len(out), fuzzCeiling)
			}
			if err != nil {
				continue
			}
			full, ferr := e.Decompress(nil, data)
			//: the unbounded decode agrees.
			if ferr != nil || !bytes.Equal(full, out) {
				t.Fatalf("%s: Decompress = %d bytes, %v; DecompressBounded = %d bytes", e.Algorithm(), len(full), ferr, len(out))
			}
		}
		for _, e := range engines {
			packed, err := e.Compress(nil, data)
			if err != nil {
				t.Fatalf("%s: Compress: %v", e.Algorithm(), err)
			}
			back, err := e.DecompressBounded(nil, packed, int64(len(data))+1)
			//: the input, back.
			if err != nil || !bytes.Equal(back, data) {
				t.Fatalf("%s: round trip of %d bytes = %d bytes, %v", e.Algorithm(), len(data), len(back), err)
			}
		}
	})
}

// fuzzZlibAgrees holds the zlib engine's own RFC 1950 envelope
// (kitsunium/sdk#1102) to compress/zlib, which it replaced on the block path:
// over any input, the engine accepts what the standard library accepts, with
// the same bytes, and refuses what it refuses, for the error the standard
// library names; a stream past the ceiling is the ceiling's refusal on both
// sides, read at max+1 bytes as the engine reads it.
func fuzzZlibAgrees(t *testing.T, engine coretransform.BoundedDecompressor, data []byte) {
	t.Helper()
	got, gerr := engine.DecompressBounded(nil, data, fuzzCeiling)
	var want []byte
	zr, werr := zlib.NewReader(bytes.NewReader(data))
	//: a header the standard library accepts has a body to read.
	if werr == nil {
		want, werr = io.ReadAll(io.LimitReader(zr, fuzzCeiling+1))
	}
	//: past the ceiling: the standard library's read is cut at max+1, and the
	//: engine refuses too. Its refusal is the ceiling's, or the decoder's own
	//: fault when the body it had already decoded ahead of the ceiling ends
	//: short: the decoder's Close reports that sticky fault, and the core folds
	//: a Close fault into the result, for gzip and flate as for zlib.
	if werr == nil && int64(len(want)) > fuzzCeiling {
		//: refused either way, and never accepted.
		if gerr == nil || (!errors.Is(gerr, coretransform.DecompressedTooLarge) && !errs.HasCode(gerr, coretransform.CodeZlibFailed)) {
			t.Fatalf("zlib: %d bytes past the ceiling, engine err=%v", len(want), gerr)
		}
		return
	}
	//: a stream the standard library refuses is refused for its reason.
	if werr != nil {
		if gerr == nil || !errors.Is(gerr, werr) {
			t.Fatalf("zlib: stdlib err=%v, engine err=%v", werr, gerr)
		}
		return
	}
	//: a stream it accepts decodes to the same bytes.
	if gerr != nil || !bytes.Equal(got, want) {
		t.Fatalf("zlib: stdlib %d bytes, engine %d bytes, err=%v", len(want), len(got), gerr)
	}
}
