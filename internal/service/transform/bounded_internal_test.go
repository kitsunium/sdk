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
	"strings"
	"testing"
)

// overflowPayloadBytes is the inflated size of the test bomb: 4 MiB of zeros
// compresses to a tiny blob yet inflates far past the lowered test cap.
const overflowPayloadBytes int = 4 << 20

// loweredCapBytes is the temporary ceiling used by the overflow tests; a few-KiB
// inflated stream trips the guard without materialising the production 256 MiB.
const loweredCapBytes int64 = 1 << 10 // 1 KiB

// errBoundedRead is the sentinel errReader returns; readAllBounded must forward
// it untouched (overflow=false, plain=nil).
var errBoundedRead = errors.New("bounded read fault")

// errReader is an io.Reader that always fails, so the read-fault branch of
// readAllBounded can be exercised.
type errReader struct{}

// Read implements io.Reader and always returns the bounded-read sentinel.
func (errReader) Read(_ []byte) (int, error) {
	//: a transport fault surfaces verbatim through readAllBounded.
	return 0, errBoundedRead
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

// boundedCase is one readAllBounded scenario: a reader, the cap to apply, and
// the expected (overflow, length, error) outcome.
type boundedCase struct {
	name         string
	reader       io.Reader
	bodyLen      int
	max          int64
	wantOverflow bool
	wantErr      error
}

// Test_readAllBounded covers the in-bounds, exactly-at-cap, over-cap, and
// read-fault branches of the shared bound through its explicit cap parameter.
// Assertions are inlined in the sub-test closure so every table field is read in
// the test body (no helper) — race-free since each case uses its own reader.
func Test_readAllBounded(t *testing.T) {
	t.Parallel()
	//: table over the cap-boundary relationships plus the read-fault branch.
	cases := []boundedCase{
		{"under-cap", strings.NewReader(strings.Repeat("x", 512)), 512, loweredCapBytes, false, nil},
		{"at-cap", strings.NewReader(strings.Repeat("x", int(loweredCapBytes))), int(loweredCapBytes), loweredCapBytes, false, nil},
		{"over-cap", strings.NewReader(strings.Repeat("x", int(loweredCapBytes)+1)), int(loweredCapBytes) + 1, loweredCapBytes, true, nil},
		{"read-fault", errReader{}, 0, loweredCapBytes, false, errBoundedRead},
	}
	//: drive every case through an inlined assertion closure.
	for _, tc := range cases {
		//: each case is independent and parallel-safe (own reader, explicit cap).
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: drive the bound with the case's reader and cap.
			plain, overflow, err := readAllBounded(tc.reader, tc.max)
			//: an error case must forward the sentinel with no overflow/buffer.
			if tc.wantErr != nil {
				//: the read fault must be forwarded verbatim with no buffer.
				if !errors.Is(err, tc.wantErr) || overflow || plain != nil {
					t.Fatalf("readAllBounded fault: err=%v overflow=%v plain=%v want %v/false/nil", err, overflow, plain, tc.wantErr)
				}
				return
			}
			//: a clean reader must never surface a read error.
			if err != nil {
				t.Fatalf("readAllBounded: unexpected error %v", err)
			}
			//: the overflow verdict must match the cap/body relationship.
			if overflow != tc.wantOverflow {
				t.Fatalf("readAllBounded(body=%d,max=%d) overflow=%v want %v", tc.bodyLen, tc.max, overflow, tc.wantOverflow)
			}
			//: an in-bounds read yields the full body; overflow yields nil.
			if !overflow && len(plain) != tc.bodyLen {
				t.Fatalf("readAllBounded: len=%d want %d", len(plain), tc.bodyLen)
			}
		})
	}
}

// Test_flateDecompress drives flateDecompress end-to-end: a clean round-trip
// under the production cap, and a bomb under a lowered cap that must trip the
// overflow→FlateFailed backstop (never a truncated success). The cap is an
// explicit parameter, so no shared state is mutated.
func Test_flateDecompress(t *testing.T) {
	t.Parallel()
	//: table over the in-bounds round-trip and the over-cap overflow.
	cases := []struct {
		name    string
		max     int64
		wantErr error
	}{
		{"under-cap-round-trips", maxDecompressedBytes, nil},
		{"over-cap-fails-closed", loweredCapBytes, FlateFailed},
	}
	//: drive each cap relationship through flateDecompress.
	for _, tc := range cases {
		//: each case is independent and parallel-safe (no shared state).
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: a 4 MiB zero bomb inflates far past the lowered cap.
			bomb := compressBomb(t, "flate", overflowPayloadBytes)
			//: the decode must match the expected sentinel (nil = success).
			if _, err := flateDecompress(nil, bomb, tc.max); err != tc.wantErr {
				t.Fatalf("flateDecompress(max=%d) err=%v want %v", tc.max, err, tc.wantErr)
			}
		})
	}
}

// Test_gzipDecompress drives gzipDecompress end-to-end: a clean round-trip under
// the production cap, and a bomb under a lowered cap that must trip the
// overflow→GzipFailed backstop.
func Test_gzipDecompress(t *testing.T) {
	t.Parallel()
	//: table over the in-bounds round-trip and the over-cap overflow.
	cases := []struct {
		name    string
		max     int64
		wantErr error
	}{
		{"under-cap-round-trips", maxDecompressedBytes, nil},
		{"over-cap-fails-closed", loweredCapBytes, GzipFailed},
	}
	//: drive each cap relationship through gzipDecompress.
	for _, tc := range cases {
		//: each case is independent and parallel-safe (no shared state).
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: a 4 MiB zero bomb inflates far past the lowered cap.
			bomb := compressBomb(t, "gzip", overflowPayloadBytes)
			//: the decode must match the expected sentinel (nil = success).
			if _, err := gzipDecompress(nil, bomb, tc.max); err != tc.wantErr {
				t.Fatalf("gzipDecompress(max=%d) err=%v want %v", tc.max, err, tc.wantErr)
			}
		})
	}
}

// Test_zlibDecompress drives zlibDecompress end-to-end: a clean round-trip under
// the production cap, and a bomb under a lowered cap that must trip the
// overflow→ZlibFailed backstop. The zlib envelope shares the DEFLATE body with
// flate, so it inherits the same bomb ratio and needs the same bound.
func Test_zlibDecompress(t *testing.T) {
	t.Parallel()
	//: table over the in-bounds round-trip and the over-cap overflow.
	cases := []struct {
		name    string
		max     int64
		wantErr error
	}{
		{"under-cap-round-trips", maxDecompressedBytes, nil},
		{"over-cap-fails-closed", loweredCapBytes, ZlibFailed},
	}
	//: drive each cap relationship through zlibDecompress.
	for _, tc := range cases {
		//: each case is independent and parallel-safe (no shared state).
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: a 4 MiB zero bomb inflates far past the lowered cap.
			bomb := compressBomb(t, "zlib", overflowPayloadBytes)
			//: the decode must match the expected sentinel (nil = success).
			if _, err := zlibDecompress(nil, bomb, tc.max); err != tc.wantErr {
				t.Fatalf("zlibDecompress(max=%d) err=%v want %v", tc.max, err, tc.wantErr)
			}
		})
	}
}
