// Package transform — white-box tests for the shared bounded-decompression
// helper and the overflow→sentinel backstop wired through gzip, flate and zlib.
// The overflow cases drive the cap-parameterised {gzip,flate,zlib}Decompress
// cores with an explicit small cap, so no shared state is mutated and the cases
// stay race-free under parallel execution.
package transform

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
	"math"
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
)

// overflowPayloadBytes is the inflated size of the test bomb: 4 MiB of zeros
// compresses to a tiny blob yet inflates far past the lowered test cap.
const overflowPayloadBytes int = 4 << 20

// loweredCapBytes is the temporary ceiling used by the overflow tests; a few-KiB
// inflated stream trips the guard without materialising the production 256 MiB.
const loweredCapBytes int64 = 1 << 10 // 1 KiB

// ceilingBodyBytes is the plaintext used by the ceiling-edge cases. It only has
// to be non-empty and cheap; the cases turn on the value of max, not the size
// of the payload.
const ceilingBodyBytes int = 4 << 10 // 4 KiB

// errBoundedRead is the sentinel errReader returns; appendAllBounded must
// forward it untouched (overflow=false, dst back as it came).
var errBoundedRead = errors.New("bounded read fault")

// errReader is an io.Reader that always fails, so the read-fault branch of
// appendAllBounded can be exercised.
type errReader struct{}

// Read implements io.Reader and always returns the bounded-read sentinel.
func (errReader) Read(_ []byte) (int, error) {
	//: a transport fault surfaces verbatim through appendAllBounded.
	return 0, errBoundedRead
}

// lateErrReader hands over n bytes, then fails: the fault arrives after the
// bytes are in dst's capacity or in a spill chunk, and must still leave dst as
// it came.
type lateErrReader struct{ n int }

// Read implements io.Reader: n bytes of 'x', then the bounded-read sentinel.
func (r *lateErrReader) Read(p []byte) (int, error) {
	//: the bytes are spent: the fault.
	if r.n == 0 {
		return 0, errBoundedRead
	}
	k := min(len(p), r.n)
	for i := range p[:k] {
		p[i] = 'x'
	}
	r.n -= k
	return k, nil
}

// compressBomb returns a scheme-compressed blob of n zero bytes.
func compressBomb(t *testing.T, scheme string, n int) []byte {
	t.Helper()
	var buf bytes.Buffer
	//: branch on the scheme to pick the matching stdlib writer.
	switch scheme {
	case "gzip":
		//: gzip writer path.
		w := gzip.NewWriter(&buf)
		//: write n zero bytes; the writer compresses them to a tiny blob.
		if _, err := w.Write(make([]byte, n)); err != nil {
			t.Fatalf("gzip write: %v", err)
		}
		//: close flushes the gzip trailer.
		if err := w.Close(); err != nil {
			t.Fatalf("gzip close: %v", err)
		}
	case "zlib":
		//: zlib writer path — same DEFLATE body under an RFC 1950 envelope.
		w := zlib.NewWriter(&buf)
		//: write n zero bytes; the writer compresses them to a tiny blob.
		if _, err := w.Write(make([]byte, n)); err != nil {
			t.Fatalf("zlib write: %v", err)
		}
		//: close flushes the Adler-32 trailer.
		if err := w.Close(); err != nil {
			t.Fatalf("zlib close: %v", err)
		}
	default:
		//: flate writer path; NewWriter only errors on an invalid level.
		w, werr := flate.NewWriter(&buf, flate.DefaultCompression)
		//: a construction fault means the test fixture itself is broken.
		if werr != nil {
			t.Fatalf("flate NewWriter: %v", werr)
		}
		//: write n zero bytes; the writer compresses them to a tiny blob.
		if _, err := w.Write(make([]byte, n)); err != nil {
			t.Fatalf("flate write: %v", err)
		}
		//: close flushes the final flate block.
		if err := w.Close(); err != nil {
			t.Fatalf("flate close: %v", err)
		}
	}
	//: hand back the compressed bomb.
	return buf.Bytes()
}

// boundedCase is one appendAllBounded scenario: a body, the dst it lands on,
// the cap to apply, and the expected (overflow, error) outcome.
type boundedCase struct {
	name string
	// bodyLen is the plaintext the reader holds; reader builds it fresh.
	bodyLen int
	// fault makes the reader fail after the body instead of ending it.
	fault bool
	// prefix is what dst holds before the call, and spare its spare capacity.
	prefix string
	spare  int
	max    int64
	// wantOverflow and wantErr are the verdict; a success returns prefix+body.
	wantOverflow bool
	wantErr      error
}

// Test_appendAllBounded covers the in-bounds, exactly-at-cap, over-cap and
// read-fault branches of the shared bound, each landing in dst's spare
// capacity, in the pooled spill chunks, and across the two — the paths
// kitsunium/sdk#1102 split the old io.ReadAll into. Every success must return
// dst's prefix followed by the body, and every refusal dst exactly as it came.
func Test_appendAllBounded(t *testing.T) {
	t.Parallel()
	chunk := spillChunkBytes
	cases := []boundedCase{
		{name: "under-cap/nil-dst", bodyLen: 512, max: loweredCapBytes},
		{name: "at-cap/nil-dst", bodyLen: int(loweredCapBytes), max: loweredCapBytes},
		{name: "over-cap/nil-dst", bodyLen: int(loweredCapBytes) + 1, max: loweredCapBytes, wantOverflow: true},
		{name: "under-cap/in-capacity", bodyLen: 512, prefix: "PFX", spare: 4096, max: loweredCapBytes},
		{name: "at-cap/exact-capacity", bodyLen: int(loweredCapBytes), prefix: "PFX", spare: int(loweredCapBytes), max: loweredCapBytes},
		{name: "over-cap/in-capacity", bodyLen: 4096, prefix: "PFX", spare: 4096, max: loweredCapBytes, wantOverflow: true},
		{name: "spills/past-capacity", bodyLen: 3*chunk + 17, prefix: "PFX", spare: 100, max: maxDecompressedBytes},
		{name: "spills/many-chunks", bodyLen: (spillKeepChunks + 3) * chunk, max: maxDecompressedBytes},
		{name: "over-cap/in-a-chunk", bodyLen: 2*chunk + 1, prefix: "PFX", spare: 100, max: int64(2 * chunk), wantOverflow: true},
		{name: "at-cap/chunk-boundary", bodyLen: 2 * chunk, max: int64(2 * chunk)},
		{name: "unbounded/max-int64", bodyLen: chunk + 5, prefix: "PFX", spare: 7, max: math.MaxInt64},
		{name: "read-fault/at-once", bodyLen: 0, fault: true, prefix: "PFX", spare: 16, max: loweredCapBytes, wantErr: errBoundedRead},
		{name: "read-fault/in-capacity", bodyLen: 10, fault: true, prefix: "PFX", spare: 64, max: loweredCapBytes, wantErr: errBoundedRead},
		{name: "read-fault/in-a-chunk", bodyLen: chunk + 10, fault: true, spare: 64, max: maxDecompressedBytes, wantErr: errBoundedRead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := bytes.Repeat([]byte{'x'}, tc.bodyLen)
			var reader io.Reader = bytes.NewReader(body)
			//: a reader that fails after the body, or at once.
			if tc.fault {
				reader = &lateErrReader{n: tc.bodyLen}
				//: the at-once case has nothing before its fault.
				if tc.bodyLen == 0 {
					reader = errReader{}
				}
			}
			var dst []byte
			//: nil stays nil: the case without a dst at all.
			if tc.prefix != "" || tc.spare > 0 {
				dst = append(make([]byte, 0, len(tc.prefix)+tc.spare), tc.prefix...)
			}
			out, overflow, err := appendAllBounded(dst, reader, tc.max)
			//: a refusal hands dst back exactly as it came.
			if tc.wantErr != nil || tc.wantOverflow {
				if !errors.Is(err, tc.wantErr) || overflow != tc.wantOverflow {
					t.Fatalf("err=%v overflow=%v, want %v/%v", err, overflow, tc.wantErr, tc.wantOverflow)
				}
				if len(out) != len(dst) || cap(out) != cap(dst) || string(out) != tc.prefix {
					t.Fatalf("a refusal returned %d bytes of capacity %d, want dst back (%d of %d)", len(out), cap(out), len(dst), cap(dst))
				}
				return
			}
			if err != nil || overflow {
				t.Fatalf("err=%v overflow=%v, want a clean read", err, overflow)
			}
			//: the prefix, then the body, byte for byte.
			if want := append([]byte(tc.prefix), body...); !bytes.Equal(out, want) {
				t.Fatalf("got %d bytes, want the %d-byte prefix and the %d-byte body", len(out), len(tc.prefix), tc.bodyLen)
			}
			//: a body that fits lands in dst's own array, never a copy.
			if len(tc.prefix)+tc.bodyLen <= cap(dst) && len(out) > 0 && &out[:1][0] != &dst[:1][0] {
				t.Fatal("a body that fitted dst's capacity was decoded into another array")
			}
		})
	}
}

// decompressBackstops is each engine's layer backstop, the cap-parameterised
// core its Decompress delegates to, keyed by the Algorithm the engine
// registers under.
var decompressBackstops = map[coretransform.Algorithm]struct {
	backstop func(dst, src []byte, max int64) ([]byte, error)
	sentinel error
}{
	"gzip":  {gzipDecompress, coretransform.GzipFailed},
	"flate": {flateDecompress, coretransform.FlateFailed},
	"zlib":  {zlibDecompress, coretransform.ZlibFailed},
}

// Test_decompressBackstop is the one row that replaced Test_flateDecompress,
// Test_gzipDecompress and Test_zlibDecompress (kitsunium/sdk#1123): for every
// engine the registry holds, a clean round-trip under the production cap, and
// a bomb under a lowered cap that trips the overflow backstop with the
// engine's sentinel — never a truncated success. The cap is an explicit
// parameter, so no shared state is mutated; an engine registered without a
// backstop in the table fails here rather than going untested.
func Test_decompressBackstop(t *testing.T) {
	t.Parallel()
	for _, algo := range coretransform.Available() {
		engine, ok := decompressBackstops[algo]
		if !ok {
			t.Errorf("engine %q is registered with no backstop in decompressBackstops", algo)
			continue
		}
		t.Run(string(algo), func(t *testing.T) {
			t.Parallel()
			//: a 4 MiB zero bomb inflates far past the lowered cap.
			bomb := compressBomb(t, string(algo), overflowPayloadBytes)
			out, err := engine.backstop(nil, bomb, maxDecompressedBytes)
			if err != nil || len(out) != overflowPayloadBytes {
				t.Fatalf("under the production cap: %d bytes, err=%v, want %d", len(out), err, overflowPayloadBytes)
			}
			dst := []byte("prefix")
			out, err = engine.backstop(dst, bomb, loweredCapBytes)
			if err != engine.sentinel {
				t.Fatalf("over the lowered cap: err=%v, want the engine's sentinel %v", err, engine.sentinel)
			}
			if string(out) != "prefix" {
				t.Fatalf("over the lowered cap: %d bytes returned, want dst back", len(out))
			}
		})
	}
}

// boundedScheme pairs a scheme name (the key compressBomb switches on) with the
// bounded decompressor under test, so the three implementations are driven
// through one table rather than three near-identical tests.
type boundedScheme struct {
	name string
	impl coretransform.BoundedDecompressor
}

// Test_DecompressBoundedHonoursEveryCeiling pins the three values of max the
// bounded contract used to get wrong, for all three schemes at once.
//
// Both defects were found by review and CONFIRMED by measurement against the
// code this test now guards, not argued from the source:
//
//	DecompressBounded(src, math.MaxInt64) -> len(out)=0 err=<nil>   [4096-byte payload]
//	DecompressBounded(src, 0)             -> len(out)=4096 err=<nil>
//
// The first is the +1 in readAllBounded wrapping int64 and turning the limit
// negative, so io.LimitReader reports EOF before the first byte: a valid stream
// decoded to nothing, successfully. The second is the old max <= 0 fallback to
// Decompress, which replaced the caller's 0-byte ceiling with this layer's
// 256 MiB backstop — the one direction a ceiling must never move.
//
// Neither is reachable from pkg/v1/data/codec, whose only call site passes a fixed
// 64 MiB: they are contract defects in an exported-within-the-module interface,
// which is why a contract test is where they belong.
func Test_DecompressBoundedHonoursEveryCeiling(t *testing.T) {
	t.Parallel()
	//: every scheme that implements the optional bounded interface.
	schemes := []boundedScheme{
		{"gzip", gzipCompressor{}},
		{"flate", flateCompressor{}},
		{"zlib", zlibCompressor{level: zlib.DefaultCompression}},
	}
	//: drive each scheme through the same four ceilings.
	for _, sc := range schemes {
		//: each sub-test owns its fixtures and is parallel-safe.
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			//: a small non-empty payload; these cases turn on max, not size.
			body := compressBomb(t, sc.name, ceilingBodyBytes)
			//: MaxInt64 is effectively unbounded and must return everything.
			out, err := sc.impl.DecompressBounded(nil, body, math.MaxInt64)
			//: a wrapped limit shows up here as a successful empty decode.
			if err != nil || len(out) != ceilingBodyBytes {
				t.Fatalf("DecompressBounded(max=MaxInt64) len=%d err=%v, want len=%d and no error", len(out), err, ceilingBodyBytes)
			}
			//: a zero ceiling holds nothing, so a non-empty stream is over cap.
			if _, zerr := sc.impl.DecompressBounded(nil, body, 0); !errors.Is(zerr, coretransform.DecompressedTooLarge) {
				t.Fatalf("DecompressBounded(max=0) err=%v, want DecompressedTooLarge", zerr)
			}
			//: a negative ceiling is clamped to zero, never widened.
			if _, nerr := sc.impl.DecompressBounded(nil, body, -1); !errors.Is(nerr, coretransform.DecompressedTooLarge) {
				t.Fatalf("DecompressBounded(max=-1) err=%v, want DecompressedTooLarge", nerr)
			}
			//: an EMPTY stream fits a zero ceiling — the bound is on bytes
			//: produced, not on the call. Without this case, "refuse
			//: everything at 0" would pass just as well as the contract.
			empty := compressBomb(t, sc.name, 0)
			//: it must succeed and produce no plaintext.
			if eout, eerr := sc.impl.DecompressBounded(nil, empty, 0); eerr != nil || len(eout) != 0 {
				t.Fatalf("DecompressBounded(empty, max=0) len=%d err=%v, want len=0 and no error", len(eout), eerr)
			}
		})
	}
}
