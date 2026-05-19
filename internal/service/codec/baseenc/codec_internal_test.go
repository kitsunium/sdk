package baseenc

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// shortWriter is a deterministic io.Writer that always reports one fewer
// byte written than the caller asked for, simulating a real-world
// short-write surface (network pipe closed mid-flight, disk-full, etc.).
// Drives the bufferingWriter.Close regression for C2.
type shortWriter struct {
	buf bytes.Buffer
}

// Write reports one fewer byte than len(p) so the caller sees a short write.
func (s *shortWriter) Write(p []byte) (n int, err error) {
	//: drain into the inner buffer so the test can inspect what was sent.
	if len(p) == 0 {
		//: zero-byte writes pass through unchanged.
		return 0, nil
	}
	//: only commit n-1 bytes; the trailing byte is lost.
	wrote := len(p) - 1
	//: commit the truncated prefix to the inner buffer.
	s.buf.Write(p[:wrote])
	//: surface the truncated count.
	return wrote, nil
}

// Test_baseencCodec_Name verifies each variant's canonical identifier.
func Test_baseencCodec_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
		want string
	}
	tests := []tc{
		{"base64", variantBase64, "base64"},
		{"base64url", variantBase64URL, "base64url"},
		{"base32", variantBase32, "base32"},
		{"base16", variantBase16, "base16"},
		{"hex", variantHex, "hex"},
		{"ascii85", variantASCII85, "ascii85"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		if got := c.Name(); got != tc.want {
			t.Errorf("%s: Name=%q want %q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_baseencCodec_MIMETypes verifies each variant's MIME alias list.
func Test_baseencCodec_MIMETypes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		v        variant
		wantHead string
	}
	tests := []tc{
		{"base64", variantBase64, "application/base64"},
		{"base64url", variantBase64URL, "application/base64url"},
		{"base32", variantBase32, "application/base32"},
		{"base16", variantBase16, "application/base16"},
		{"hex", variantHex, "application/hex"},
		{"ascii85", variantASCII85, "application/ascii85"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		mimes := c.MIMETypes()
		if len(mimes) == 0 || mimes[0] != tc.wantHead {
			t.Errorf("%s: MIMETypes=%v, want head %q", tc.name, mimes, tc.wantHead)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_baseencCodec_Extensions verifies each variant's file extension list.
func Test_baseencCodec_Extensions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		v        variant
		wantHead string
	}
	tests := []tc{
		{"base64", variantBase64, ".b64"},
		{"base64url", variantBase64URL, ".b64url"},
		{"base32", variantBase32, ".b32"},
		{"base16", variantBase16, ".b16"},
		{"hex", variantHex, ".hex"},
		{"ascii85", variantASCII85, ".a85"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		exts := c.Extensions()
		if len(exts) == 0 || exts[0] != tc.wantHead {
			t.Errorf("%s: Extensions=%v, want head %q", tc.name, exts, tc.wantHead)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_baseencCodec_encodeBytes verifies each variant's encoder yields a
// non-empty buffer and the result is shape-compatible with its decoder.
func Test_baseencCodec_encodeBytes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64", variantBase64},
		{"base64url", variantBase64URL},
		{"base32", variantBase32},
		{"base16", variantBase16},
		{"hex", variantHex},
		{"ascii85", variantASCII85},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		raw := []byte("payload-bytes-for-roundtrip")
		got := c.encodeBytes(raw)
		if len(got) == 0 {
			t.Fatalf("%s: encodeBytes returned empty buffer", tc.name)
		}
		back, derr := c.decodeBytes(got)
		if derr != nil {
			t.Fatalf("%s: decodeBytes err=%v", tc.name, derr)
		}
		if !bytes.Equal(back, raw) {
			t.Errorf("%s: round-trip mismatch got=%q want=%q", tc.name, back, raw)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_baseencCodec_appendEncode verifies the in-place append helpers
// preserve the prefix bytes and produce the same output as encodeBytes.
func Test_baseencCodec_appendEncode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64", variantBase64},
		{"base64url", variantBase64URL},
		{"base32", variantBase32},
		{"base16", variantBase16},
		{"hex", variantHex},
		{"ascii85", variantASCII85},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		raw := []byte("payload")
		prefix := []byte("prefix:")
		want := slices.Clone(prefix)
		want = append(want, c.encodeBytes(raw)...)
		got := c.appendEncode(slices.Clone(prefix), raw)
		if !bytes.Equal(got, want) {
			t.Errorf("%s: appendEncode=%q want=%q", tc.name, got, want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_baseencCodec_decodeBytes_malformed surfaces BASE_ENC_DECODE_FAILED
// for every variant whose decoder can reject input.
func Test_baseencCodec_decodeBytes_malformed(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
		data []byte
	}
	tests := []tc{
		{"base64", variantBase64, []byte("!!!notb64!!!")},
		{"base64url", variantBase64URL, []byte("!!!notb64!!!")},
		{"base32", variantBase32, []byte("!!!notb32!!!")},
		{"base16", variantBase16, []byte("ZZZZ")},
		{"hex", variantHex, []byte("ZZZZ")},
		{"ascii85", variantASCII85, []byte{0x00, 0x01, 0xFF}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		//: contract: every variant maps decoder failures onto the typed
		//: CodeBaseEncDecodeFailed sentinel — asserting only `err != nil`
		//: would mask a regression where the wrong code surfaced.
		_, err := c.decodeBytes(tc.data)
		if err == nil {
			t.Errorf("%s: expected decode error, got nil", tc.name)
			return
		}
		if !errs.HasCode(err, CodeBaseEncDecodeFailed) {
			got, _ := errs.CodeOf(err)
			t.Errorf("%s: decode error code = %s, want %s",
				tc.name, got, CodeBaseEncDecodeFailed)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_bufferingWriter_RoundTrip drives the base16 streaming path that
// uses bufferingWriter to delay encoding until Close.
func Test_bufferingWriter_RoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"buffered writer flushes on close"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: variantBase16}
		var buf bytes.Buffer
		w := c.streamWriter(&buf)
		if _, err := w.Write([]byte("data")); err != nil {
			t.Fatalf("%s: Write err=%v", tc.name, err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("%s: Close err=%v", tc.name, err)
		}
		if buf.Len() == 0 {
			t.Errorf("%s: writer produced no bytes", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_bufferingWriter_ShortWrite_C2 surfaces io.ErrShortWrite wrapped
// with BASE_ENC_MARSHAL_FAILED when the underlying writer accepts only
// part of the encoded payload. Previously Close silently truncated.
func Test_bufferingWriter_ShortWrite_C2(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"short write surfaces io.ErrShortWrite"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: variantBase16}
		dst := &shortWriter{}
		w := &bufferingWriter{dst: dst, codec: c}
		if _, err := w.Write([]byte("data")); err != nil {
			t.Fatalf("%s: Write err=%v", tc.name, err)
		}
		err := w.Close()
		if err == nil {
			t.Fatalf("%s: expected error on short write", tc.name)
		}
		//: wrap chain must carry io.ErrShortWrite.
		if !errors.Is(err, io.ErrShortWrite) {
			t.Errorf("%s: expected io.ErrShortWrite in chain, got %v", tc.name, err)
		}
		//: wrap chain must carry the marshal-failed reason.
		if !errs.HasReason(err, "BASE_ENC_MARSHAL_FAILED") {
			t.Errorf("%s: expected BASE_ENC_MARSHAL_FAILED, got %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_baseencDecoder_More returns true before the first Decode is called
// so streaming consumers can probe input availability cheaply.
func Test_baseencDecoder_More(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"reports true before first Decode"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		d := &baseencDecoder{src: bytes.NewReader(nil), v: variantBase64}
		if !d.More() {
			t.Errorf("%s: More() = false before first Decode, want true", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
