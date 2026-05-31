// Package codec_test — black-box coverage for the compressed-frame verbs.
package codec_test

import (
	"testing"

	codec "github.com/kitsunium/sdk/pkg/v1/codec"
	errs "github.com/kitsunium/sdk/pkg/v1/errs"
)

// sampleRecord is a small JSON-shaped value exercised through the compressed
// round-trip; both fields must survive Marshal → compress → decompress → decode.
type sampleRecord struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

// runCompressedRoundTrip compresses want under (format, algo), decompresses it
// back, and fails the test unless the decoded record equals want.
func runCompressedRoundTrip(t *testing.T, format codec.Format, algo codec.CompressAlgorithm, want sampleRecord) {
	t.Helper()
	//: compress through the universal codec dispatch + transform registry.
	box, err := codec.MarshalCompressed(format, algo, want)
	//: a round-trip case must not fail the compress leg.
	if err != nil {
		//: surface the unexpected compress error.
		t.Fatalf("MarshalCompressed(%s,%s): unexpected error %v", format, algo, err)
	}
	//: the first byte must be the frozen frame magic.
	if len(box) == 0 || box[0] != 0xC7 {
		//: a missing magic means the frame header was not written.
		t.Fatalf("MarshalCompressed(%s,%s): box missing frame magic 0xC7", format, algo)
	}
	//: decode the frame back into a fresh record.
	var got sampleRecord
	//: decompress + decode must succeed and reproduce want.
	if derr := codec.UnmarshalCompressed(box, &got); derr != nil {
		//: surface the unexpected decompress error.
		t.Fatalf("UnmarshalCompressed(%s,%s): unexpected error %v", format, algo, derr)
	}
	//: the recovered record must equal the original.
	if got != want {
		//: a mismatch means the round-trip lost or corrupted data.
		t.Fatalf("round-trip(%s,%s): got %+v want %+v", format, algo, got, want)
	}
}

// runMarshalCompressedCase drives one MarshalCompressed scenario: a round-trip
// when wantErr is empty, otherwise an error-reason assertion.
func runMarshalCompressedCase(t *testing.T, algo codec.CompressAlgorithm, rec sampleRecord, wantErr string) {
	t.Helper()
	//: an empty wantErr means a full round-trip is expected.
	if wantErr == "" {
		//: success path — round-trip must reproduce rec.
		runCompressedRoundTrip(t, codec.JSON, algo, rec)
		return
	}
	//: error path — only the compress leg is exercised.
	_, err := codec.MarshalCompressed(codec.JSON, algo, rec)
	//: the reported reason must match the expected sentinel.
	if !errs.HasReason(err, wantErr) {
		//: surface the wrong / missing error.
		t.Fatalf("MarshalCompressed: got %v want reason %s", err, wantErr)
	}
}

// Test_MarshalCompressed covers the happy round-trip per algorithm plus the
// unknown-algorithm rejection.
func Test_MarshalCompressed(t *testing.T) {
	t.Parallel()
	//: rec is the shared payload reused across every sub-case.
	rec := sampleRecord{Name: "kitsune", N: 42}
	//: cases pair an algorithm (or a bogus one) with the expected reason.
	cases := []struct {
		name    string
		algo    codec.CompressAlgorithm
		wantErr string
	}{
		{"gzip", codec.Gzip, ""},
		{"flate", codec.Flate, ""},
		{"unknown-algo", codec.CompressAlgorithm("zstd"), "UNKNOWN_COMPRESSOR"},
	}
	//: drive every case through the shared runner.
	for _, tc := range cases {
		//: each case is independent and runs in parallel with its siblings.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: delegate to the shared MarshalCompressed runner.
			runMarshalCompressedCase(t, tc.algo, rec, tc.wantErr)
		})
	}
}

// Test_UnmarshalCompressed covers structural frame rejections; the happy path is
// exercised by Test_MarshalCompressed's round-trip.
func Test_UnmarshalCompressed(t *testing.T) {
	t.Parallel()
	//: good is a real gzip+JSON frame reused to derive malformed variants.
	good, err := codec.MarshalCompressed(codec.JSON, codec.Gzip, sampleRecord{Name: "x", N: 1})
	//: the fixture itself must encode cleanly.
	if err != nil {
		//: a broken fixture invalidates every sub-case.
		t.Fatalf("fixture MarshalCompressed: %v", err)
	}
	//: each case feeds a malformed box and expects CompressedFrameInvalid.
	cases := []struct {
		name string
		box  []byte
	}{
		{"too-short", []byte{0xC7, 0x01}},
		{"bad-magic", append([]byte{0x00}, good[1:]...)},
		{"bad-version", append([]byte{0xC7, 0x02}, good[2:]...)},
		{"unknown-algid", append([]byte{0xC7, 0x01, 0x7F}, good[3:]...)},
		{"len-overrun", []byte{0xC7, 0x01, 0x01, 0xFF, 0xFF}},
	}
	//: drive every malformed box through UnmarshalCompressed.
	for _, tc := range cases {
		//: each malformed frame must be rejected, never decoded.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: decode target is irrelevant; the frame is rejected first.
			var sink sampleRecord
			//: a structural defect must surface COMPRESSED_FRAME_INVALID.
			if uerr := codec.UnmarshalCompressed(tc.box, &sink); !errs.HasReason(uerr, "COMPRESSED_FRAME_INVALID") {
				//: surface the wrong / missing rejection.
				t.Fatalf("UnmarshalCompressed(%s): got %v want COMPRESSED_FRAME_INVALID", tc.name, uerr)
			}
		})
	}
}
