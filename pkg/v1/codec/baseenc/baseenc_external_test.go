package baseenc_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/codec/baseenc"
)

// TestEncoding_String covers Encoding.String() for every named value plus
// the EncodingUnknown sentinel and an out-of-range value.
func TestEncoding_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   baseenc.Encoding
		want string
	}
	tests := []tc{
		{"EncodingUnknown maps to unknown", baseenc.EncodingUnknown, "unknown"},
		{"Base64Std maps to base64", baseenc.Base64Std, "base64"},
		{"Base64URL maps to base64url", baseenc.Base64URL, "base64url"},
		{"Base32Std maps to base32", baseenc.Base32Std, "base32"},
		{"Base32Hex maps to base32hex", baseenc.Base32Hex, "base32hex"},
		{"Base16Hex maps to hex", baseenc.Base16Hex, "hex"},
		{"ASCII85 maps to ascii85", baseenc.ASCII85, "ascii85"},
		{"out-of-range value maps to unknown", baseenc.Encoding(99), "unknown"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if got := tc.in.String(); got != tc.want {
			t.Errorf("%s: String() = %q, want %q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestEncode covers every registered Encoding plus the INVALID_ENCODING
// failure path.
func TestEncode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		enc     baseenc.Encoding
		raw     []byte
		wantErr string
	}
	tests := []tc{
		{"base64 standard alphabet", baseenc.Base64Std, []byte("hi"), ""},
		{"base64 URL alphabet", baseenc.Base64URL, []byte("hi"), ""},
		{"base32 standard alphabet", baseenc.Base32Std, []byte("hi"), ""},
		{"base32 hex alphabet", baseenc.Base32Hex, []byte("hi"), ""},
		{"hex lowercase", baseenc.Base16Hex, []byte("hi"), ""},
		{"ascii85", baseenc.ASCII85, []byte("hi"), ""},
		{"unknown encoding", baseenc.EncodingUnknown, []byte("hi"), "INVALID_ENCODING"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := baseenc.Encode(tc.enc, tc.raw)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: Encode err=%v", tc.name, err)
		}
		if tc.wantErr != "" && !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDecode covers every registered Encoding plus INVALID_ENCODING and
// DECODE_FAILED failure paths.
func TestDecode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		enc     baseenc.Encoding
		text    string
		wantErr string
	}
	tests := []tc{
		{"base64 standard roundtrip", baseenc.Base64Std, "aGk=", ""},
		{"base64 URL roundtrip", baseenc.Base64URL, "aGk=", ""},
		{"hex roundtrip", baseenc.Base16Hex, "6869", ""},
		{"ascii85 roundtrip", baseenc.ASCII85, "BOu!r", ""},
		{"unknown encoding", baseenc.EncodingUnknown, "x", "INVALID_ENCODING"},
		{"base64 bad bytes", baseenc.Base64Std, "???", "DECODE_FAILED"},
		{"hex bad bytes", baseenc.Base16Hex, "zz", "DECODE_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := baseenc.Decode(tc.enc, tc.text)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: Decode err=%v", tc.name, err)
		}
		if tc.wantErr != "" && !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
