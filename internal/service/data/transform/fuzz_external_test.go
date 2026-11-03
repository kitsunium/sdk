package transform_test

import (
	"bytes"
	"compress/flate"
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
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
	f.Fuzz(func(t *testing.T, data []byte) {
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
