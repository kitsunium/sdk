package baseenc_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/codec/baseenc"
)

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		enc  baseenc.Encoding
	}
	tests := []tc{
		{"base64", baseenc.Base64Std},
		{"base64url", baseenc.Base64URL},
		{"base32", baseenc.Base32Std},
		{"base32hex", baseenc.Base32Hex},
		{"hex", baseenc.Base16Hex},
		{"ascii85", baseenc.ASCII85},
	}
	raw := []byte("Hello, baseenc!")
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		text, err := baseenc.Encode(c.enc, raw)
		if err != nil {
			t.Fatalf("%s Encode: %v", c.name, err)
		}
		out, err := baseenc.Decode(c.enc, text)
		if err != nil {
			t.Fatalf("%s Decode: %v", c.name, err)
		}
		if string(out) != string(raw) {
			t.Errorf("%s round-trip: got %q want %q", c.name, out, raw)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestEncode_KnownValues(t *testing.T) {
	t.Parallel()
	text, err := baseenc.Encode(baseenc.Base64Std, []byte("hi"))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if text != "aGk=" {
		t.Errorf("got %q want %q", text, "aGk=")
	}
}

func TestEncode_UnknownEncoding(t *testing.T) {
	t.Parallel()
	_, err := baseenc.Encode(baseenc.Encoding("nope"), []byte("x"))
	if !errs.HasReason(err, "INVALID_ENCODING") {
		t.Errorf("expected INVALID_ENCODING, got %v", err)
	}
}

func TestDecode_UnknownEncoding(t *testing.T) {
	t.Parallel()
	_, err := baseenc.Decode(baseenc.Encoding("nope"), "x")
	if !errs.HasReason(err, "INVALID_ENCODING") {
		t.Errorf("expected INVALID_ENCODING, got %v", err)
	}
}

func TestDecode_BadBytes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		enc  baseenc.Encoding
		text string
	}
	tests := []tc{
		{"base64", baseenc.Base64Std, "???"},
		{"base32", baseenc.Base32Std, "1111"},
		{"hex", baseenc.Base16Hex, "zz"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, err := baseenc.Decode(c.enc, c.text)
		if !errs.HasReason(err, "DECODE_FAILED") {
			t.Errorf("%s: expected DECODE_FAILED, got %v", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
