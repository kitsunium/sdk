package transform

import (
	"bytes"
	"compress/zlib"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_zlibCompressor_Algorithm pins the scheme's canonical identifier.
func Test_zlibCompressor_Algorithm(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"algorithm is zlib", "zlib"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the concrete scheme must report its canonical Algorithm.
		if got := (zlibCompressor{}).Algorithm(); string(got) != c.want {
			t.Errorf("%s: Algorithm()=%q want %q", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_zlibCompressor_Compress exercises the encode path: a real payload
// produces non-empty wire bytes, and a repetitive payload shrinks.
func Test_zlibCompressor_Compress(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		payload    []byte
		wantShrink bool
	}
	tests := []tc{
		{"small payload encodes", []byte("compress me"), false},
		{"repetitive payload shrinks", bytes.Repeat([]byte("AAAA"), 8192), true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		boxed, err := NewZlibCompressor(zlib.DefaultCompression).Compress(nil, c.payload)
		if err != nil {
			t.Fatalf("%s: Compress err=%v", c.name, err)
		}
		//: a repetitive payload must compress strictly smaller than its input.
		if c.wantShrink && len(boxed) >= len(c.payload) {
			t.Errorf("%s: compressed %d >= input %d", c.name, len(boxed), len(c.payload))
		}
		//: any non-empty payload must produce a non-empty zlib frame.
		if len(boxed) == 0 {
			t.Errorf("%s: empty zlib frame for non-empty payload", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_zlibCompressor_Decompress exercises the decode path: a round-trip
// reproduces the original, and garbage input surfaces ZlibFailed.
func Test_zlibCompressor_Decompress(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		payload []byte
		garbage bool
	}
	tests := []tc{
		{"round-trip empty", []byte{}, false},
		{"round-trip text", []byte("decompress me back"), false},
		{"garbage fails", []byte("not a zlib stream at all"), true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		z := zlibCompressor{level: zlib.DefaultCompression}
		//: the garbage arm feeds raw bytes straight into Decompress.
		if c.garbage {
			if _, err := z.Decompress(nil, c.payload); err == nil {
				t.Errorf("%s: expected error on garbage, got nil", c.name)
			}
			return
		}
		boxed, cerr := z.Compress(nil, c.payload)
		if cerr != nil {
			t.Fatalf("%s: seed Compress err=%v", c.name, cerr)
		}
		got, derr := z.Decompress(nil, boxed)
		if derr != nil {
			t.Fatalf("%s: Decompress err=%v", c.name, derr)
		}
		//: an empty payload round-trips to an empty slice.
		if len(c.payload) == 0 && len(got) == 0 {
			return
		}
		if !bytes.Equal(got, c.payload) {
			t.Errorf("%s: round-trip mismatch", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_NewZlibCompressor_NeverInert is the ADR 0031 regression guard: whatever
// level a caller supplies — including the in-range-but-inert NoCompression and
// levels the stdlib writer rejects outright — the constructor hands back a
// compressor that actually compresses and round-trips. The assertion is on the
// OBSERVABLE outcome (bytes shrink, payload survives), never on the clamped
// field, so it survives a change of clamping mechanism.
func Test_NewZlibCompressor_NeverInert(t *testing.T) {
	t.Parallel()
	//: a repetitive payload is the shrink oracle: any real level compresses it.
	payload := bytes.Repeat([]byte("kitsunium-sdk "), 4096)
	type tc struct {
		name  string
		level int
	}
	tests := []tc{
		{"default", zlib.DefaultCompression},
		{"huffman-only", zlib.HuffmanOnly},
		{"best-speed", zlib.BestSpeed},
		{"best-compression", zlib.BestCompression},
		{"no-compression is in-range yet inert", zlib.NoCompression},
		{"below the accepted band", zlib.HuffmanOnly - 1},
		{"above the accepted band", zlib.BestCompression + 1},
		{"absurdly out of band", 1 << 20},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		comp := NewZlibCompressor(c.level)
		//: an out-of-band level must never survive into the writer as an error.
		boxed, cerr := comp.Compress(nil, payload)
		if cerr != nil {
			t.Fatalf("%s: Compress err=%v", c.name, cerr)
		}
		//: the whole point — the compressor must actually compress.
		if len(boxed) >= len(payload) {
			t.Errorf("%s: compressed %d >= input %d (inert compressor)", c.name, len(boxed), len(payload))
		}
		//: clamping must not cost correctness: the payload still round-trips.
		got, derr := comp.Decompress(nil, boxed)
		if derr != nil {
			t.Fatalf("%s: Decompress err=%v", c.name, derr)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("%s: round-trip mismatch (got %d, want %d)", c.name, len(got), len(payload))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_zlibCompressor_CompressBadLevel drives the writer-construction fault path
// that NewZlibCompressor's clamp makes unreachable. Reaching it needs an
// in-package struct literal, which is the point: the branch exists so a future
// scheme that skips the constructor still fails typed rather than silently, and
// this test is what keeps the clamp honest about being the only thing guarding
// it.
func Test_zlibCompressor_CompressBadLevel(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		level      int
		wantReason string
	}
	tests := []tc{
		{"below the accepted band", zlib.HuffmanOnly - 1, "ZLIB_FAILED"},
		{"above the accepted band", zlib.BestCompression + 1, "ZLIB_FAILED"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the literal bypasses NewZlibCompressor, so the bad level reaches zlib.
		_, err := zlibCompressor{level: c.level}.Compress(nil, []byte("payload"))
		//: an unclamped level must fail typed under the zlib sentinel.
		if err == nil {
			t.Fatalf("%s: expected an error for level %d, got nil", c.name, c.level)
		}
		if !errs.HasReason(err, c.wantReason) {
			t.Errorf("%s: expected %s, got %v", c.name, c.wantReason, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
