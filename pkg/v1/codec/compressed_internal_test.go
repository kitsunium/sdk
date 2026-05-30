// Package codec — white-box coverage for the compressed-frame helpers.
package codec

import (
	"bytes"
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/transform"
)

// Test_frameAlgID checks the algorithm → frozen id mapping plus the
// unknown-algorithm miss.
func Test_frameAlgID(t *testing.T) {
	t.Parallel()
	//: each case pairs an algorithm with its expected id + presence flag.
	cases := []struct {
		name   string
		algo   CompressAlgorithm
		wantID byte
		wantOK bool
	}{
		{"gzip", Gzip, algGzip, true},
		{"flate", Flate, algFlate, true},
		{"unknown", CompressAlgorithm("zstd"), 0, false},
	}
	//: drive every case through frameAlgID.
	for _, tc := range cases {
		//: id + ok must both match the expectation.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: call the helper under test.
			id, ok := frameAlgID(tc.algo)
			//: a divergence on either return is a failure.
			if id != tc.wantID || ok != tc.wantOK {
				//: surface the mismatch.
				t.Fatalf("frameAlgID(%s) = (%d,%v) want (%d,%v)", tc.algo, id, ok, tc.wantID, tc.wantOK)
			}
		})
	}
}

// Test_algorithmForID checks the frozen id → algorithm inverse mapping and the
// unknown-id miss.
func Test_algorithmForID(t *testing.T) {
	t.Parallel()
	//: each case pairs a frame id with its expected algorithm + presence flag.
	cases := []struct {
		name     string
		id       byte
		wantAlgo CompressAlgorithm
		wantOK   bool
	}{
		{"gzip", algGzip, Gzip, true},
		{"flate", algFlate, Flate, true},
		{"unknown", 0x7F, "", false},
	}
	//: drive every case through algorithmForID.
	for _, tc := range cases {
		//: algorithm + ok must both match the expectation.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: call the helper under test.
			algo, ok := algorithmForID(tc.id)
			//: a divergence on either return is a failure.
			if algo != tc.wantAlgo || ok != tc.wantOK {
				//: surface the mismatch.
				t.Fatalf("algorithmForID(%d) = (%q,%v) want (%q,%v)", tc.id, algo, ok, tc.wantAlgo, tc.wantOK)
			}
		})
	}
}

// Test_isBomb checks each branch of the decompression-bomb guard: absolute
// ceiling, small-payload floor, and the expansion-ratio bound.
func Test_isBomb(t *testing.T) {
	t.Parallel()
	//: each case names an (inLen,outLen) pair and the expected verdict.
	cases := []struct {
		name   string
		inLen  int
		outLen int
		want   bool
	}{
		{"over-ceiling", 1, maxDecompressedFrameBytes + 1, true},
		{"under-floor", 1, bombFloorBytes, false},
		{"ratio-trip", 100, 100*maxExpansionRatio + 1, true},
		{"within-ratio", 100, bombFloorBytes + 1, false},
	}
	//: drive every case through isBomb.
	for _, tc := range cases {
		//: the verdict must match the expectation.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: call the guard under test.
			if got := isBomb(tc.inLen, tc.outLen); got != tc.want {
				//: surface the wrong verdict.
				t.Fatalf("isBomb(%d,%d) = %v want %v", tc.inLen, tc.outLen, got, tc.want)
			}
		})
	}
}

// Test_appendFrame verifies that a rendered frame parses back to its inputs,
// covering appendFrame and parseFrame together as inverse operations.
func Test_appendFrame(t *testing.T) {
	t.Parallel()
	//: each case renders a frame around a body then parses it back.
	cases := []struct {
		name string
		id   byte
		algo CompressAlgorithm
		f    Format
		body []byte
	}{
		{"gzip-json", algGzip, Gzip, JSON, []byte("compressed-body")},
		{"flate-empty", algFlate, Flate, JSON, []byte{}},
	}
	//: drive every case through appendFrame + parseFrame.
	for _, tc := range cases {
		//: the parsed components must reproduce the rendered inputs.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: render then parse the frame.
			algo, f, payload, ok := parseFrame(appendFrame(tc.id, tc.f, tc.body))
			//: a well-formed frame must parse and reproduce every component.
			if !ok || algo != tc.algo || f != tc.f || !bytes.Equal(payload, tc.body) {
				//: surface any divergence between rendered and parsed frame.
				t.Fatalf("round-trip: (%v,%q,%q,%q)", ok, algo, f, payload)
			}
		})
	}
}

// Test_parseFrame checks the structural rejections directly (the happy path is
// covered by Test_appendFrame).
func Test_parseFrame(t *testing.T) {
	t.Parallel()
	//: each case is a malformed buffer that must fail to parse.
	cases := []struct {
		name string
		box  []byte
	}{
		{"too-short", []byte{0xC7, 0x01}},
		{"bad-magic", []byte{0x00, 0x01, algGzip, 0x00, 0x00}},
		{"bad-version", []byte{0xC7, 0x02, algGzip, 0x00, 0x00}},
		{"unknown-id", []byte{0xC7, 0x01, 0x7F, 0x00, 0x00}},
		{"len-overrun", []byte{0xC7, 0x01, algGzip, 0xFF, 0xFF}},
	}
	//: every malformed buffer must report ok=false.
	for _, tc := range cases {
		//: call parseFrame and assert rejection.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: a structural defect must yield ok=false.
			if _, _, _, ok := parseFrame(tc.box); ok {
				//: surface the frame that should have been rejected.
				t.Fatalf("parseFrame(%s): ok=true, want false", tc.name)
			}
		})
	}
}

// Test_compressFrame checks that compressFrame produces a frame whose payload
// decompresses back to the original input, for each registered algorithm.
func Test_compressFrame(t *testing.T) {
	t.Parallel()
	//: plain is the codec output the frame will carry once compressed.
	plain := []byte(`{"name":"x","n":1}`)
	//: each case compresses + decompresses under one algorithm id.
	cases := []struct {
		name string
		id   byte
		algo CompressAlgorithm
	}{
		{"gzip", algGzip, Gzip},
		{"flate", algFlate, Flate},
	}
	//: drive every case through compressFrame + decompressBounded.
	for _, tc := range cases {
		//: the payload must decompress cleanly back to plain.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: render a frame around plain under this algorithm.
			frame, err := compressFrame(JSON, tc.id, plain)
			//: a registered compressor must not fail here.
			if err != nil {
				//: surface the unexpected compress error.
				t.Fatalf("compressFrame: unexpected error %v", err)
			}
			//: parse the frame, then decompress its payload.
			_, _, payload, ok := parseFrame(frame)
			//: a frame from compressFrame must always parse.
			if !ok {
				//: surface the unparseable frame.
				t.Fatalf("compressFrame produced an unparseable frame")
			}
			//: decompress + bomb-guard must recover plain exactly.
			out, derr := decompressBounded(tc.algo, payload)
			//: a mismatch or error is a round-trip failure.
			if derr != nil || !bytes.Equal(out, plain) {
				//: surface the decompression mismatch.
				t.Fatalf("decompressBounded: (%v) out=%q want %q", derr, out, plain)
			}
		})
	}
}

// Test_decompressBounded checks the corrupt-payload and unknown-algorithm
// rejections (the happy path is covered by Test_compressFrame).
func Test_decompressBounded(t *testing.T) {
	t.Parallel()
	//: each case feeds a payload that must surface an error.
	cases := []struct {
		name    string
		algo    CompressAlgorithm
		payload []byte
	}{
		{"corrupt-body", Gzip, []byte("not a gzip stream")},
		{"unknown-algo", CompressAlgorithm("zstd"), []byte("ignored")},
	}
	//: drive every case through decompressBounded.
	for _, tc := range cases {
		//: each case must report an error, never a panic.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: a corrupt body or unregistered algorithm must fail.
			if _, err := decompressBounded(tc.algo, tc.payload); err == nil {
				//: a missing error is a failure.
				t.Fatalf("decompressBounded(%s): nil error, want failure", tc.name)
			}
		})
	}
	//: an unregistered algorithm must surface the exact missing-compressor sentinel.
	if _, err := decompressBounded(CompressAlgorithm("zstd"), nil); err != coretransform.UnknownCompressor {
		//: surface the wrong / missing sentinel.
		t.Fatalf("decompressBounded(zstd): got %v want UnknownCompressor", err)
	}
}
