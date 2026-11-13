package transform_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"runtime"
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	tptransform "github.com/kitsunium/sdk/third-party/transform"
)

// boundedCeiling is the caller's ceiling every bounded bomb below is read
// under: a sixty-fourth of bombPlain, far below the compressors' own.
const boundedCeiling int64 = 1 << 20

// heapAllocatedBy reports the bytes the process allocated on the heap while f
// ran. It is process-wide, so the tests that read it are not parallel: Go runs
// them before any parallel test resumes, and their bounds are an order of
// magnitude apart from what an unbounded decode would allocate.
func heapAllocatedBy(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// zstdBombs returns three 64 MiB zstd bombs: one frame announcing its size
// (what Compress writes), the same frame with its announced size forged down
// to 4 KiB, and a stream that announces nothing (what NewWriter writes).
func zstdBombs(t *testing.T) map[string][]byte {
	t.Helper()
	builder, err := tptransform.NewZstdCompressor(tptransform.ZstdFastest, int64(bombPlain)*2)
	if err != nil {
		t.Fatalf("build bomb: %v", err)
	}
	defer closeChecked(t, builder)
	plain := make([]byte, bombPlain)
	sized, err := builder.Compress(nil, plain)
	if err != nil {
		t.Fatalf("compress bomb: %v", err)
	}
	//: the descriptor after the magic: a 4-byte content size, no dictionary,
	//: a window descriptor — the layout EncodeAll writes for 64 MiB.
	if desc := sized[4]; desc>>6 != 2 || desc&0x20 != 0 || desc&0x03 != 0 {
		t.Fatalf("unexpected frame header descriptor %#x", desc)
	}
	forged := bytes.Clone(sized)
	binary.LittleEndian.PutUint32(forged[6:10], 4096)
	var stream bytes.Buffer
	w, err := builder.NewWriter(&stream)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatalf("write bomb stream: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close bomb stream: %v", err)
	}
	return map[string][]byte{"sized": sized, "forged-size": forged, "stream": stream.Bytes()}
}

// TestZstdDecompressBoundedRefusesABombBeforeAllocatingIt is #917's first
// acceptance line: under a caller's ceiling far below the compressor's own,
// a 64 MiB bomb is refused and the output past the ceiling is never
// allocated. A frame announcing its size is refused from its header, so the
// refusal allocates nothing like the payload; a forged size and a stream
// that announces none are read under the ceiling, so what is allocated is
// the ceiling's worth of output and the decoder's window, never the 64 MiB.
func TestZstdDecompressBoundedRefusesABombBeforeAllocatingIt(t *testing.T) {
	bombs := zstdBombs(t)
	//: the compressor's own ceiling is above the bomb: only the caller's bounds it.
	c, err := tptransform.NewZstdCompressor(tptransform.ZstdFastest, int64(bombPlain)*2)
	if err != nil {
		t.Fatalf("NewZstdCompressor: %v", err)
	}
	defer closeChecked(t, c)
	for _, tc := range []struct {
		name  string
		bound uint64
		// tooLarge is whether the refusal must be DecompressedTooLarge: the
		// forged frame may also be refused as the corrupt frame it is.
		tooLarge bool
	}{
		{"sized", 1 << 20, true},
		{"forged-size", 16 << 20, false},
		{"stream", 16 << 20, true},
	} {
		bomb := bombs[tc.name]
		dst := []byte("prefix")
		var out []byte
		var derr error
		allocated := heapAllocatedBy(func() { out, derr = c.DecompressBounded(dst, bomb, boundedCeiling) })
		t.Logf("%s: %d compressed bytes, %d bytes allocated by the refusal", tc.name, len(bomb), allocated)
		if derr == nil {
			t.Fatalf("%s: a 64 MiB bomb accepted under a 1 MiB ceiling: %d bytes", tc.name, len(out))
		}
		if tc.tooLarge && !errors.Is(derr, coretransform.DecompressedTooLarge) {
			t.Fatalf("%s: err=%v, want DecompressedTooLarge", tc.name, derr)
		}
		if !bytes.Equal(out, dst) {
			t.Fatalf("%s: a refusal returned %d bytes, want dst back", tc.name, len(out))
		}
		if allocated > tc.bound {
			t.Fatalf("%s: the refusal allocated %d bytes, want at most %d: the output was not bounded before it was allocated", tc.name, allocated, tc.bound)
		}
	}
}

// TestZstdDecompressBoundedHoldsTheCallersCeiling: exactly max is admitted
// and one byte less refused, into a nil dst, a prefix and a prefix with the
// spare capacity the decode lands in; two frames are bounded together; an
// empty payload passes a ceiling of zero and a negative one.
func TestZstdDecompressBoundedHoldsTheCallersCeiling(t *testing.T) {
	t.Parallel()
	c, err := tptransform.NewZstdCompressor(tptransform.ZstdDefault, tptransform.DefaultMaxDecompressedBytes)
	if err != nil {
		t.Fatalf("NewZstdCompressor: %v", err)
	}
	defer closeChecked(t, c)
	plain := bytes.Repeat([]byte(`{"k":"v","n":12345}`), 1000)
	one, _ := c.Compress(nil, plain)
	two := append(bytes.Clone(one), one...)
	both := append(bytes.Clone(plain), plain...)
	for _, tc := range []struct {
		name string
		src  []byte
		want []byte
	}{{"one-frame", one, plain}, {"two-frames", two, both}} {
		size := int64(len(tc.want))
		for _, dst := range [][]byte{nil, []byte("prefix"), append(make([]byte, 0, 6+len(tc.want)), "prefix"...)} {
			out, derr := c.DecompressBounded(dst, tc.src, size)
			if derr != nil || !bytes.Equal(out[len(dst):], tc.want) || !bytes.Equal(out[:len(dst)], dst) {
				t.Fatalf("%s at its size, dst %d/%d: err=%v, %d bytes", tc.name, len(dst), cap(dst), derr, len(out))
			}
			for _, ceiling := range []int64{size - 1, 1, 0, -1} {
				out, derr := c.DecompressBounded(dst, tc.src, ceiling)
				if !errors.Is(derr, coretransform.DecompressedTooLarge) || !bytes.Equal(out, dst) {
					t.Fatalf("%s under %d: err=%v, %d bytes, want DecompressedTooLarge and dst back", tc.name, ceiling, derr, len(out))
				}
			}
		}
	}
	empty, _ := c.Compress(nil, nil)
	for _, ceiling := range []int64{0, -1} {
		if out, derr := c.DecompressBounded([]byte("p"), empty, ceiling); derr != nil || string(out) != "p" {
			t.Fatalf("empty payload under %d: err=%v, %q", ceiling, derr, out)
		}
	}
}

// TestDecompressBoundedKeepsTheCompressorsCeiling: a caller's ceiling above
// the compressor's own does not lift it — the payload past the compressor's
// is refused as Decompress refuses it, by both schemes.
func TestDecompressBoundedKeepsTheCompressorsCeiling(t *testing.T) {
	t.Parallel()
	z, err := tptransform.NewZstdCompressor(tptransform.ZstdFastest, bombLimit)
	if err != nil {
		t.Fatalf("NewZstdCompressor: %v", err)
	}
	defer closeChecked(t, z)
	s, err := tptransform.NewS2Compressor(bombLimit)
	if err != nil {
		t.Fatalf("NewS2Compressor: %v", err)
	}
	plain := make([]byte, 2*bombLimit)
	for _, c := range []coretransform.BoundedDecompressor{z, s} {
		enc, _ := c.Compress(nil, plain)
		out, derr := c.DecompressBounded(nil, enc, 4*bombLimit)
		if !errs.HasCode(derr, tptransform.CodeDecompressionLimitExceeded) || len(out) != 0 {
			t.Fatalf("%s: err=%v, %d bytes, want DecompressionLimitExceeded", c.Algorithm(), derr, len(out))
		}
	}
}

// TestS2DecompressBoundedRefusesFromTheHeader: s2's block declares its
// length, so a bomb under a tighter ceiling is refused before anything the
// size of its payload is allocated.
func TestS2DecompressBoundedRefusesFromTheHeader(t *testing.T) {
	builder, err := tptransform.NewS2Compressor(int64(bombPlain) * 2)
	if err != nil {
		t.Fatalf("NewS2Compressor: %v", err)
	}
	bomb, _ := builder.Compress(nil, make([]byte, bombPlain))
	var out []byte
	var derr error
	allocated := heapAllocatedBy(func() { out, derr = builder.DecompressBounded(nil, bomb, boundedCeiling) })
	if !errors.Is(derr, coretransform.DecompressedTooLarge) || len(out) != 0 {
		t.Fatalf("err=%v, %d bytes, want DecompressedTooLarge", derr, len(out))
	}
	if allocated > 1<<20 {
		t.Fatalf("the refusal allocated %d bytes", allocated)
	}
}

// TestZstdDecompressBoundedReadsDictionariesAndRefusesCorruption: the
// bounded path classifies as Decompress does — a dictionary frame read with
// its dictionary, refused DictionaryMismatch without, a corrupt frame
// ZstdFailed.
func TestZstdDecompressBoundedReadsDictionariesAndRefusesCorruption(t *testing.T) {
	t.Parallel()
	withDict := newDictionaryCompressor(t, trainDictionary(t, 917, dictionaryRecords(t, 300)))
	plain := []byte(`{"level":"info","msg":"request served","status":200}`)
	enc, _ := withDict.Compress(nil, plain)
	if out, derr := withDict.DecompressBounded(nil, enc, int64(len(plain))); derr != nil || !bytes.Equal(out, plain) {
		t.Fatalf("with its dictionary: err=%v, %q", derr, out)
	}
	zstd := tptransform.Zstd.(coretransform.BoundedDecompressor)
	if _, derr := zstd.DecompressBounded(nil, enc, 1<<20); !errs.HasCode(derr, tptransform.CodeDictionaryMismatch) {
		t.Fatalf("without its dictionary: err=%v, want DictionaryMismatch", derr)
	}
	plainEnc, _ := zstd.Compress(nil, bytes.Repeat(plain, 50))
	corrupt := bytes.Clone(plainEnc)
	corrupt[len(corrupt)/2] ^= 0xff
	if out, derr := zstd.DecompressBounded([]byte("p"), corrupt, 1<<20); !errs.HasCode(derr, tptransform.CodeZstdFailed) || string(out) != "p" {
		t.Fatalf("corrupt frame: err=%v, %d bytes, want ZstdFailed and dst back", derr, len(out))
	}
}

// TestTheRegisteredSchemesAreBounded: the registry publishes the bounded
// sibling — what a consumer finds under "zstd" and "s2", consensus's
// snapshot codec among them, is a BoundedDecompressor.
func TestTheRegisteredSchemesAreBounded(t *testing.T) {
	t.Parallel()
	for _, algo := range []coretransform.Algorithm{tptransform.ZstdAlgorithm, tptransform.S2Algorithm} {
		c, ok := coretransform.Lookup(algo)
		if !ok {
			t.Fatalf("%s not registered", algo)
		}
		if _, ok := c.(coretransform.BoundedDecompressor); !ok {
			t.Fatalf("%s is not a BoundedDecompressor", algo)
		}
	}
}
