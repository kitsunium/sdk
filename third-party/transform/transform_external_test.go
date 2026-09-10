package transform_test

import (
	"bytes"
	"crypto/rand"
	"slices"
	"sync"
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	tptransform "github.com/kitsunium/sdk/third-party/transform"
)

// closeChecked releases a compressor and fails the test if the release itself
// failed. Discarding a Close error in a test is how a resource leak stays
// invisible until it is somebody else's flake.
func closeChecked(tb testing.TB, closer interface{ Close() error }) {
	tb.Helper()
	if err := closer.Close(); err != nil {
		tb.Errorf("close: %v", err)
	}
}

// scheme pairs a registered algorithm with a freshly built compressor, so every
// contract test below runs against both without being written twice.
type scheme struct {
	algo       coretransform.Algorithm
	compress   func(dst, src []byte) ([]byte, error)
	decompress func(dst, src []byte) ([]byte, error)
}

// schemes builds one compressor per registered algorithm and registers their
// release with the test's cleanup.
func schemes(tb testing.TB) []scheme {
	tb.Helper()
	z, zerr := tptransform.NewZstdCompressor(tptransform.ZstdFastest, tptransform.DefaultMaxDecompressedBytes)
	if zerr != nil {
		tb.Fatalf("build zstd: %v", zerr)
	}
	tb.Cleanup(func() { closeChecked(tb, z) })
	s, serr := tptransform.NewS2Compressor(tptransform.DefaultMaxDecompressedBytes)
	if serr != nil {
		tb.Fatalf("build s2: %v", serr)
	}
	return []scheme{
		{tptransform.ZstdAlgorithm, z.Compress, z.Decompress},
		{tptransform.S2Algorithm, s.Compress, s.Decompress},
	}
}

// payload is one shape in the round-trip corpus.
type payload struct {
	name  string
	bytes []byte
}

// payloads is the corpus every round-trip runs over: the shapes that break
// compressors in practice, not just the happy one.
func payloads(tb testing.TB) []payload {
	tb.Helper()
	random := make([]byte, 1<<16)
	if _, err := rand.Read(random); err != nil {
		tb.Fatalf("rand: %v", err)
	}
	return []payload{
		{"empty", []byte{}},
		{"single byte", []byte{0x00}},
		{"ascii", []byte("the quick brown fox jumps over the lazy dog")},
		{"json-ish", bytes.Repeat([]byte(`{"k":"v","n":1234,"b":true},`), 512)},
		{"all zeros", bytes.Repeat([]byte{0}, 1<<16)},
		{"all 0xff", bytes.Repeat([]byte{0xff}, 1<<16)},
		{"incompressible", random},
		{"binary with nuls and newlines", append(
			bytes.Repeat([]byte{0x00, 0x0a, 0x0d, 0x1a}, 4096),
			[]byte("trailing\n\x00")...)},
		{"invalid utf-8", []byte{0xc3, 0x28, 0xa0, 0xa1, 0xe2, 0x28, 0xa1, 0xf0, 0x28, 0x8c, 0x28}},
	}
}

// TestRoundTripIsExact is the codec domain's round-trip contract applied to the
// transform port: Decompress(Compress(x)) is x, byte for byte, for every shape
// in the corpus.
//
// The empty payload is in the corpus on purpose — it is the case where a
// compressor is most tempted to return nil and where a caller distinguishing
// nil from empty gets burned.
func TestRoundTripIsExact(t *testing.T) {
	t.Parallel()

	for _, sc := range schemes(t) {
		for _, p := range payloads(t) {
			t.Run(string(sc.algo)+"/"+p.name, func(t *testing.T) {
				t.Parallel()
				encoded, cerr := sc.compress(nil, p.bytes)
				if cerr != nil {
					t.Fatalf("compress: %v", cerr)
				}
				back, derr := sc.decompress(nil, encoded)
				if derr != nil {
					t.Fatalf("decompress: %v", derr)
				}
				if !bytes.Equal(back, p.bytes) {
					t.Fatalf("round trip lost bytes: got %d, want %d", len(back), len(p.bytes))
				}
			})
		}
	}
}

// TestCompressPreservesDstPrefix pins the append-to-dst convention on the
// COMPRESS side. It is the single most valuable test in this file, because the
// bug it prevents is invisible to every test that passes dst=nil — and s2's
// Encode treats its first argument as scratch, so getting this wrong silently
// destroys the caller's buffer rather than failing.
func TestCompressPreservesDstPrefix(t *testing.T) {
	t.Parallel()

	prefix := []byte("KEEP-ME-EXACTLY-AS-I-AM")
	plain := bytes.Repeat([]byte("payload"), 1000)

	for _, sc := range schemes(t) {
		t.Run(string(sc.algo), func(t *testing.T) {
			t.Parallel()
			out, cerr := sc.compress(slices.Clone(prefix), plain)
			if cerr != nil {
				t.Fatalf("compress: %v", cerr)
			}
			if !bytes.HasPrefix(out, prefix) {
				t.Fatalf("dst prefix destroyed: got %q", out[:min(len(out), len(prefix))])
			}
			//: and the appended tail must still be a valid, exact frame.
			back, derr := sc.decompress(nil, out[len(prefix):])
			if derr != nil {
				t.Fatalf("decompress the appended tail: %v", derr)
			}
			if !bytes.Equal(back, plain) {
				t.Fatalf("tail is not the payload: got %d bytes, want %d", len(back), len(plain))
			}
		})
	}
}

// TestDecompressPreservesDstPrefix is the same claim on the decompress side.
func TestDecompressPreservesDstPrefix(t *testing.T) {
	t.Parallel()

	prefix := []byte("PREFIX")
	plain := bytes.Repeat([]byte("payload"), 1000)

	for _, sc := range schemes(t) {
		t.Run(string(sc.algo), func(t *testing.T) {
			t.Parallel()
			encoded, cerr := sc.compress(nil, plain)
			if cerr != nil {
				t.Fatalf("compress: %v", cerr)
			}
			out, derr := sc.decompress(slices.Clone(prefix), encoded)
			if derr != nil {
				t.Fatalf("decompress: %v", derr)
			}
			want := append(slices.Clone(prefix), plain...)
			if !bytes.Equal(out, want) {
				t.Fatalf("dst prefix + payload mismatch: got %d bytes, want %d", len(out), len(want))
			}
		})
	}
}

// TestSchemesAreRegistered proves the blank-import contract: importing this
// package makes both algorithms resolvable through the core registry, under the
// exact names the constants declare.
func TestSchemesAreRegistered(t *testing.T) {
	t.Parallel()

	for _, algo := range []coretransform.Algorithm{tptransform.ZstdAlgorithm, tptransform.S2Algorithm} {
		c, ok := coretransform.Lookup(algo)
		if !ok {
			t.Fatalf("%q is not registered", algo)
		}
		if c.Algorithm() != algo {
			t.Fatalf("%q resolves to a compressor calling itself %q", algo, c.Algorithm())
		}
		if !algo.Known() {
			t.Fatalf("%q registered but not Known()", algo)
		}
		if !slices.Contains(coretransform.Available(), algo) {
			t.Fatalf("%q missing from Available()", algo)
		}
	}
}

// TestSchemesAreNotWireCompatible pins that the two formats are distinct
// envelopes, not aliases. A stream produced by one must be REFUSED by the
// other, not silently mis-decoded — the interop bug internal/service/transform
// documents at length for flate versus zlib, checked here before it can be
// repeated with two new names.
func TestSchemesAreNotWireCompatible(t *testing.T) {
	t.Parallel()

	plain := bytes.Repeat([]byte("cross-scheme"), 500)
	sc := schemes(t)
	zstd, s2c := sc[0], sc[1]

	zEncoded, err := zstd.compress(nil, plain)
	if err != nil {
		t.Fatalf("zstd compress: %v", err)
	}
	sEncoded, err := s2c.compress(nil, plain)
	if err != nil {
		t.Fatalf("s2 compress: %v", err)
	}

	if out, derr := s2c.decompress(nil, zEncoded); derr == nil {
		t.Fatalf("s2 accepted a zstd frame and returned %d bytes", len(out))
	}
	if out, derr := zstd.decompress(nil, sEncoded); derr == nil {
		t.Fatalf("zstd accepted an s2 block and returned %d bytes", len(out))
	}
}

// TestConcurrentUseIsSafe drives one shared compressor from many goroutines,
// because that is exactly how the registered singleton will be used and the
// library's concurrency guarantee is the reason a singleton is legitimate at
// all. Run under -race this is the assertion; the byte comparison is the rest.
func TestConcurrentUseIsSafe(t *testing.T) {
	t.Parallel()

	for _, sc := range schemes(t) {
		t.Run(string(sc.algo), func(t *testing.T) {
			t.Parallel()
			var wg sync.WaitGroup
			for i := range 16 {
				wg.Go(func() {
					plain := bytes.Repeat([]byte{byte(i)}, 4096+i)
					encoded, cerr := sc.compress(nil, plain)
					if cerr != nil {
						t.Errorf("compress: %v", cerr)
						return
					}
					back, derr := sc.decompress(nil, encoded)
					if derr != nil {
						t.Errorf("decompress: %v", derr)
						return
					}
					if !bytes.Equal(back, plain) {
						t.Errorf("goroutine %d: round trip lost bytes", i)
					}
				})
			}
			wg.Wait()
		})
	}
}

// TestTruncatedStreamIsRefused checks the other half of the security surface:
// a stream cut short must fail typed, never return a partial payload as if it
// were the whole thing.
func TestTruncatedStreamIsRefused(t *testing.T) {
	t.Parallel()

	plain := bytes.Repeat([]byte("truncate me"), 2000)

	for _, sc := range schemes(t) {
		t.Run(string(sc.algo), func(t *testing.T) {
			t.Parallel()
			encoded, cerr := sc.compress(nil, plain)
			if cerr != nil {
				t.Fatalf("compress: %v", cerr)
			}
			truncated := encoded[:len(encoded)/2]
			out, derr := sc.decompress(nil, truncated)
			if derr == nil {
				t.Fatalf("truncated stream accepted: %d bytes", len(out))
			}
			if len(out) != 0 {
				t.Fatalf("truncated stream leaked %d bytes", len(out))
			}
			if _, typed := errs.CodeOf(derr); !typed {
				t.Fatalf("untyped error from a truncated stream: %v", derr)
			}
		})
	}
}

// TestZstdLevelNeverYieldsAnInertCompressor pins ADR 0031's clamp half for the
// one knob that has one. The assertion is the OBSERVABLE outcome — bytes
// actually shrink and the payload round-trips — never the clamped field, so it
// survives a change of mechanism.
func TestZstdLevelNeverYieldsAnInertCompressor(t *testing.T) {
	t.Parallel()

	//: compressible enough that any working level must shrink it.
	plain := bytes.Repeat([]byte("compress me please "), 4096)

	for _, level := range []tptransform.ZstdLevel{
		tptransform.ZstdFastest, tptransform.ZstdDefault, tptransform.ZstdBetter, tptransform.ZstdBest,
		//: the values ADR 0031 is actually about: out of range, and zero.
		0, -7, 99, 1 << 30,
	} {
		c, err := tptransform.NewZstdCompressor(level, tptransform.DefaultMaxDecompressedBytes)
		if err != nil {
			t.Fatalf("level %d refused: %v", level, err)
		}
		encoded, cerr := c.Compress(nil, plain)
		if cerr != nil {
			closeChecked(t, c)
			t.Fatalf("level %d: compress: %v", level, cerr)
		}
		if len(encoded) >= len(plain) {
			closeChecked(t, c)
			t.Fatalf("level %d is inert: %d bytes in, %d out", level, len(plain), len(encoded))
		}
		back, derr := c.Decompress(nil, encoded)
		if derr != nil || !bytes.Equal(back, plain) {
			closeChecked(t, c)
			t.Fatalf("level %d: round trip failed: %v", level, derr)
		}
		closeChecked(t, c)
	}
}

// TestNoInternalImportNeeded is a compile-time claim in test form: an external
// consumer reaches the compressors without importing internal/core/transform.
// The whole test body uses only tptransform identifiers and inferred types, so
// if the surface ever required naming an internal type this file would stop
// compiling.
//
// The coretransform import elsewhere in this file is the registry check, which
// only the SDK's own tests can perform.
func TestNoInternalImportNeeded(t *testing.T) {
	t.Parallel()

	c, err := tptransform.NewS2Compressor(tptransform.DefaultMaxDecompressedBytes)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	//: the Algorithm type comes from an internal package; inference names it.
	algo := c.Algorithm()
	if algo.String() != "s2" {
		t.Fatalf("algorithm: got %q", algo.String())
	}
	//: and the singleton is usable the same way.
	if tptransform.Zstd.Algorithm().String() != "zstd" {
		t.Fatalf("zstd singleton: got %q", tptransform.Zstd.Algorithm().String())
	}
}
