// Package codec_test — black-box coverage for the compressed-frame verbs.
package codec_test

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
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

// Test_UnmarshalCompressed_CorruptInnerPayloadForwardsOrigin_V76 locks the
// compressed.go doc contract ("codec and compressor faults are forwarded
// untouched (origin wins)"). The frame's gzip body is valid and decompresses
// cleanly to corrupt JSON, so the inner json codec — not the gzip transform —
// rejects it. That fault must surface as UNMARSHAL_FAILED, not the
// PROMOTE_FAILED that masked it before the V75 root-cause fix.
func Test_UnmarshalCompressed_CorruptInnerPayloadForwardsOrigin_V76(t *testing.T) {
	t.Parallel()
	//: each row pairs an inner Format with a corrupt payload that the inner
	//: codec — reached after a clean decompress — must reject.
	cases := []struct {
		name   string
		format string
		inner  []byte
	}{
		{"corrupt-inner-json", "json", []byte("{ this is not json")},
	}
	//: drive every framed corrupt payload through UnmarshalCompressed.
	for _, tc := range cases {
		//: the inner codec fault must reach the caller unmasked.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: assemble a frame whose decompression succeeds so the inner
			//: codec is the component that rejects the payload.
			frame := buildCompressedFrameV76(tc.format, gzipPayloadV76(t, tc.inner))
			//: decode target is irrelevant; the inner decode fails first.
			var sink map[string]any
			//: capture the forwarded fault once.
			uerr := codec.UnmarshalCompressed(frame, &sink)
			//: a fault must be returned — the inner payload is corrupt.
			if uerr == nil {
				//: silent success means the corruption was swallowed.
				t.Fatalf("%s: expected an error, got nil", tc.name)
			}
			//: the inner codec's origin reason must survive (V76).
			if !errs.HasReason(uerr, "UNMARSHAL_FAILED") {
				//: a missing origin reason means it was replaced en route.
				t.Fatalf("%s: want forwarded UNMARSHAL_FAILED, got %v", tc.name, uerr)
			}
			//: the masking sentinel must not leak through the wrapper.
			if errs.HasReason(uerr, "PROMOTE_FAILED") {
				//: PROMOTE_FAILED here is the V76 transitive regression.
				t.Fatalf("%s: PROMOTE_FAILED masked the inner fault (V76)", tc.name)
			}
		})
	}
}

// gzipPayloadV76 returns raw compressed with stdlib gzip, the same body shape
// MarshalCompressed writes for the gzip algorithm, so a hand-built frame
// decompresses cleanly and exercises the inner codec directly.
func gzipPayloadV76(t *testing.T, raw []byte) []byte {
	t.Helper()
	//: collect the compressed bytes in memory.
	var buf bytes.Buffer
	//: standard gzip writer matches the SDK's gzip transform body.
	w := gzip.NewWriter(&buf)
	//: write the raw inner bytes into the compressor.
	if _, err := w.Write(raw); err != nil {
		//: a write failure is a harness fault, not the contract.
		t.Fatalf("gzip write failed: %v", err)
	}
	//: Close flushes the gzip trailer — required for a valid stream.
	if err := w.Close(); err != nil {
		//: a close failure is likewise a harness fault.
		t.Fatalf("gzip close failed: %v", err)
	}
	//: the buffered bytes are a complete gzip stream.
	return buf.Bytes()
}

// buildCompressedFrameV76 assembles the frozen self-describing frame layout
// (magic 0xC7, version 0x01, gzip algID 0x01, 2-byte BE inner-Format length,
// inner Format, compressed body) so a test controls the body independently of
// MarshalCompressed.
func buildCompressedFrameV76(format string, body []byte) []byte {
	//: magic + frame version + gzip algorithm id.
	frame := []byte{0xC7, 0x01, 0x01}
	//: 2-byte big-endian inner-Format length field.
	lenField := make([]byte, 2)
	//: stamp the inner Format name length.
	binary.BigEndian.PutUint16(lenField, uint16(len(format)))
	//: append the length field, then the inner Format, then the body.
	frame = append(frame, lenField...)
	//: inner Format string travels next so Unmarshal needs no Format arg.
	frame = append(frame, []byte(format)...)
	//: compressed body closes the frame.
	return append(frame, body...)
}
