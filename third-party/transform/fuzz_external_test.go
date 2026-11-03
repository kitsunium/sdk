package transform_test

import (
	"bytes"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	tptransform "github.com/kitsunium/sdk/third-party/transform"
)

// fuzzCeiling is the decompression ceiling the fuzz compressors are built
// with: small enough that a seed bomb crosses it, large enough for the
// round-trip inputs.
const fuzzCeiling int64 = 64 << 10

// fuzzDstPrefix is the caller's prefix every Decompress must leave intact.
const fuzzDstPrefix = "dst-prefix"

// fuzzCompressor is the slice of the two compressors the targets drive.
type fuzzCompressor interface {
	Compress(dst, src []byte) ([]byte, error)
	Decompress(dst, src []byte) ([]byte, error)
}

// fuzzCompressors builds one zstd compressor per level and one s2, all at
// fuzzCeiling.
func fuzzCompressors(f *testing.F) []fuzzCompressor {
	f.Helper()
	var out []fuzzCompressor
	for _, level := range []tptransform.ZstdLevel{tptransform.ZstdFastest, tptransform.ZstdDefault, tptransform.ZstdBetter, tptransform.ZstdBest} {
		z, err := tptransform.NewZstdCompressor(level, fuzzCeiling)
		if err != nil {
			f.Fatalf("NewZstdCompressor: %v", err)
		}
		out = append(out, z)
	}
	s, err := tptransform.NewS2Compressor(fuzzCeiling)
	if err != nil {
		f.Fatalf("NewS2Compressor: %v", err)
	}
	return append(out, s)
}

// fuzzSeeds compresses the plaintexts the suite uses, a bomb past the
// ceiling among them, with every compressor: valid frames, then the same
// frames cut short.
func fuzzSeeds(f *testing.F, compressors []fuzzCompressor) [][]byte {
	f.Helper()
	plains := [][]byte{nil, []byte("hello, transform"), bytes.Repeat([]byte("abc"), 4096), make([]byte, 4*fuzzCeiling)}
	var seeds [][]byte
	for _, c := range compressors {
		for _, p := range plains {
			frame, err := c.Compress(nil, p)
			if err != nil {
				f.Fatalf("Compress: %v", err)
			}
			seeds = append(seeds, frame, frame[:len(frame)/2])
		}
	}
	return append(seeds, []byte{0x28, 0xb5, 0x2f, 0xfd}, []byte("sNaPpY"), []byte{0xff, 0x06, 0x00, 0x00})
}

// fuzzDst is one shape of the caller's dst a Decompress is handed, and the
// bytes its prefix must still hold afterwards.
type fuzzDst struct {
	name   string
	prefix []byte
	make   func() []byte
}

// fuzzDsts returns the dst shapes the targets decode into — the ones the tail
// treatment (#1163) tells apart: no spare capacity, where the decode outgrows
// dst and is appended once; a little, where a short payload lands in place and
// a long one outgrows it; room for the whole ceiling, where every payload
// under it lands in dst's own array; and a prefix already longer than the
// ceiling, which must not count against it. The long prefix is built once and
// handed out clipped, so no decode can write into its array; the others are
// fresh on every call, because a decode writes into their spare capacity.
func fuzzDsts() []fuzzDst {
	short := []byte(fuzzDstPrefix)
	long := bytes.Repeat(short, int(fuzzCeiling)/len(short)+1)
	pristine := slices.Clone(long)
	withRoom := func(spare int) func() []byte {
		return func() []byte { return append(make([]byte, 0, len(short)+spare), short...) }
	}
	return []fuzzDst{
		{"no-spare", short, func() []byte { return slices.Clip([]byte(fuzzDstPrefix)) }},
		{"small-spare", short, withRoom(16)},
		{"ceiling-spare", short, withRoom(int(fuzzCeiling))},
		{"longer-than-the-ceiling", pristine, func() []byte { return slices.Clip(long) }},
	}
}

// FuzzDecompressNeverExceedsCeiling is #1159: zstd and s2 decode untrusted
// bytes, and nothing proved "no panic, a typed code on every failure, dst
// untouched on refusal" beyond three fixtures. Any input, to any of the five
// compressors, into every dst shape of fuzzDsts — the decode into dst's tail
// of #1163 included: the result never grows past dst plus the ceiling, dst's
// prefix is intact, and a refusal is ZSTD_FAILED, S2_FAILED,
// DECOMPRESSION_LIMIT_EXCEEDED or — for a frame naming a dictionary these
// compressors do not hold (#900) — DICTIONARY_MISMATCH, with dst handed back
// at its own length. testdata/fuzz holds the frame that taught it the last.
func FuzzDecompressNeverExceedsCeiling(f *testing.F) {
	compressors := fuzzCompressors(f)
	for _, seed := range fuzzSeeds(f, compressors) {
		f.Add(seed)
	}
	dsts := fuzzDsts()
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, c := range compressors {
			for _, d := range dsts {
				dst := d.make()
				out, err := c.Decompress(dst, data)
				if int64(len(out)) > int64(len(dst))+fuzzCeiling {
					t.Fatalf("%T, %s: returned %d bytes past a %d-byte ceiling", c, d.name, len(out)-len(dst), fuzzCeiling)
				}
				if len(out) < len(dst) || !bytes.Equal(out[:len(dst)], d.prefix) {
					t.Fatalf("%T, %s: clobbered the caller's prefix", c, d.name)
				}
				if err == nil {
					continue
				}
				if !typedRefusal(err) {
					t.Fatalf("%T, %s: untyped refusal %v", c, d.name, err)
				}
				if len(out) != len(dst) {
					t.Fatalf("%T, %s: a refusal returned %d bytes past dst", c, d.name, len(out)-len(dst))
				}
			}
		}
	})
}

// typedRefusal reports whether err carries one of the codes a Decompress of
// untrusted bytes may refuse with.
func typedRefusal(err error) bool {
	for _, code := range []errs.Code{
		tptransform.CodeZstdFailed, tptransform.CodeS2Failed,
		tptransform.CodeDecompressionLimitExceeded, tptransform.CodeDictionaryMismatch,
	} {
		if errs.HasCode(err, code) {
			return true
		}
	}
	return false
}

// FuzzRoundTripIsExact is #1159's other half: Decompress(Compress(x)) is x,
// after the caller's prefix, for both schemes at every zstd level and into
// every dst shape of fuzzDsts — so a payload landing in dst's spare capacity
// (#1163) and one outgrowing it read back the same.
func FuzzRoundTripIsExact(f *testing.F) {
	compressors := fuzzCompressors(f)
	for _, seed := range [][]byte{nil, []byte("hello, transform"), bytes.Repeat([]byte("abc"), 4096), {0x00, 0xff, 0x00}} {
		f.Add(seed)
	}
	dsts := fuzzDsts()
	f.Fuzz(func(t *testing.T, plain []byte) {
		if int64(len(plain)) > fuzzCeiling {
			return
		}
		for _, c := range compressors {
			frame, err := c.Compress(nil, plain)
			if err != nil {
				t.Fatalf("%T Compress: %v", c, err)
			}
			for _, d := range dsts {
				dst := d.make()
				out, derr := c.Decompress(dst, frame)
				if derr != nil || len(out) != len(dst)+len(plain) || !bytes.Equal(out[:len(dst)], d.prefix) || !bytes.Equal(out[len(dst):], plain) {
					t.Fatalf("%T, %s: round trip of %d bytes: %d bytes back (%v)", c, d.name, len(plain), len(out)-len(dst), derr)
				}
			}
		}
	})
}
