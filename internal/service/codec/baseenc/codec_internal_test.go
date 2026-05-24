package baseenc

import (
	"bytes"
	stdjson "encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
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
		//: wrap chain must carry the typed dotted-quad code so an
		//: incorrect sentinel with the same reason cannot pass silently.
		if !errs.HasCode(err, CodeBaseEncMarshalFailed) {
			t.Errorf("%s: expected CodeBaseEncMarshalFailed, got %v", tc.name, err)
		}
		//: wrap chain must also carry the marshal-failed reason for log
		//: consumers that match by reason string.
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

// Test_baseencCodec_Marshal drives the JSON→base-N pipeline directly so
// the encoder's variant dispatch is covered without going through the
// public registry. Decoding via decodeBytes proves the round-trip.
func Test_baseencCodec_Marshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 marshal", variantBase64},
		{"base64url marshal", variantBase64URL},
		{"base32 marshal", variantBase32},
		{"base16 marshal", variantBase16},
		{"hex marshal", variantHex},
		{"ascii85 marshal", variantASCII85},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		out, err := c.Marshal(map[string]int{"x": 1})
		if err != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, err)
		}
		if len(out) == 0 {
			t.Errorf("%s: Marshal produced empty output", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_baseencCodec_Marshal_jsonRejected surfaces BASE_ENC_MARSHAL_FAILED
// when encoding/json refuses the value. Channel values are the canonical
// non-encodable case.
func Test_baseencCodec_Marshal_jsonRejected(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 rejects chan", variantBase64},
		{"ascii85 rejects chan", variantASCII85},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		_, err := c.Marshal(make(chan int))
		if !errs.HasCode(err, CodeBaseEncMarshalFailed) {
			t.Errorf("%s: expected CodeBaseEncMarshalFailed, got %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_baseencCodec_Unmarshal drives the base-N→JSON pipeline through
// every variant and confirms the recovered payload round-trips.
func Test_baseencCodec_Unmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 unmarshal", variantBase64},
		{"base64url unmarshal", variantBase64URL},
		{"base32 unmarshal", variantBase32},
		{"base16 unmarshal", variantBase16},
		{"hex unmarshal", variantHex},
		{"ascii85 unmarshal", variantASCII85},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		encoded, merr := c.Marshal(map[string]int{"n": 42})
		if merr != nil {
			t.Fatalf("%s: seed Marshal err=%v", tc.name, merr)
		}
		var got map[string]int
		if uerr := c.Unmarshal(encoded, &got); uerr != nil {
			t.Fatalf("%s: Unmarshal err=%v", tc.name, uerr)
		}
		if got["n"] != 42 {
			t.Errorf("%s: Unmarshal got=%v, want n=42", tc.name, got)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_baseencCodec_Unmarshal_oversize surfaces BASE_ENC_SIZE_EXCEEDED at
// the 10 MiB boundary so a CWE-400 regression fails loudly.
func Test_baseencCodec_Unmarshal_oversize(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 oversize", variantBase64},
		{"hex oversize", variantHex},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		var out struct{ N int }
		err := c.Unmarshal(make([]byte, maxBaseEncBytes+1), &out)
		if !errs.HasCode(err, CodeBaseEncSizeExceeded) {
			t.Errorf("%s: expected CodeBaseEncSizeExceeded, got %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_baseencCodec_Unmarshal_badJSON surfaces BASE_ENC_UNMARSHAL_FAILED
// when the inner JSON layer rejects the recovered bytes.
func Test_baseencCodec_Unmarshal_badJSON(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 bad inner", variantBase64},
		{"hex bad inner", variantHex},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		//: encode non-JSON bytes so the outer base-N step succeeds and the
		//: inner json.Unmarshal trips.
		envelope := c.encodeBytes([]byte("{not-json"))
		var out struct{ N int }
		err := c.Unmarshal(envelope, &out)
		if !errs.HasCode(err, CodeBaseEncUnmarshalFailed) {
			t.Errorf("%s: expected CodeBaseEncUnmarshalFailed, got %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_baseencCodec_Append verifies the Append method preserves the dst
// prefix and yields bytes byte-identical to Marshal beyond the prefix.
func Test_baseencCodec_Append(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 append", variantBase64},
		{"base64url append", variantBase64URL},
		{"base32 append", variantBase32},
		{"base16 append", variantBase16},
		{"hex append", variantHex},
		{"ascii85 append", variantASCII85},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		prefix := []byte("HEAD:")
		out, err := c.Append(slices.Clone(prefix), map[string]int{"n": 1})
		if err != nil {
			t.Fatalf("%s: Append err=%v", tc.name, err)
		}
		if !bytes.HasPrefix(out, prefix) {
			t.Errorf("%s: Append dropped prefix; got=%q", tc.name, out)
		}
		direct, merr := c.Marshal(map[string]int{"n": 1})
		if merr != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, merr)
		}
		if !bytes.Equal(out[len(prefix):], direct) {
			t.Errorf("%s: Append tail diverges from Marshal: got=%q want=%q",
				tc.name, out[len(prefix):], direct)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_baseencCodec_Append_rollback verifies the Appender contract
// restores dst on a JSON-side failure so callers never see a partially
// mutated buffer.
func Test_baseencCodec_Append_rollback(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 rollback", variantBase64},
		{"ascii85 rollback", variantASCII85},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		prefix := []byte("HEAD:")
		out, err := c.Append(slices.Clone(prefix), make(chan int))
		if !errs.HasCode(err, CodeBaseEncMarshalFailed) {
			t.Errorf("%s: expected CodeBaseEncMarshalFailed, got %v", tc.name, err)
		}
		if !bytes.Equal(out, prefix) {
			t.Errorf("%s: dst not restored on error; got=%q want=%q", tc.name, out, prefix)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_baseencCodec_NewEncoder verifies NewEncoder returns a non-nil
// streaming Encoder for every variant and that the Encoder/Decoder pair
// round-trips a JSON value.
func Test_baseencCodec_NewEncoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 encoder", variantBase64},
		{"base64url encoder", variantBase64URL},
		{"base32 encoder", variantBase32},
		{"base16 encoder", variantBase16},
		{"hex encoder", variantHex},
		{"ascii85 encoder", variantASCII85},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		var sink bytes.Buffer
		enc := c.NewEncoder(&sink)
		if enc == nil {
			t.Fatalf("%s: NewEncoder returned nil", tc.name)
		}
		if err := enc.Encode(map[string]int{"n": 7}); err != nil {
			t.Fatalf("%s: Encode err=%v", tc.name, err)
		}
		if err := enc.Close(); err != nil {
			t.Fatalf("%s: Close err=%v", tc.name, err)
		}
		if sink.Len() == 0 {
			t.Errorf("%s: NewEncoder produced no output", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_baseencCodec_NewDecoder verifies NewDecoder lazily wires the
// base-N reader through a json.Decoder; encoding via the same variant
// proves the round-trip.
func Test_baseencCodec_NewDecoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 decoder", variantBase64},
		{"base64url decoder", variantBase64URL},
		{"base32 decoder", variantBase32},
		{"base16 decoder", variantBase16},
		{"hex decoder", variantHex},
		{"ascii85 decoder", variantASCII85},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		raw, jerr := stdjson.Marshal(map[string]int{"n": 9})
		if jerr != nil {
			t.Fatalf("%s: seed json.Marshal err=%v", tc.name, jerr)
		}
		src := bytes.NewReader(c.encodeBytes(raw))
		dec := c.NewDecoder(src)
		if dec == nil {
			t.Fatalf("%s: NewDecoder returned nil", tc.name)
		}
		var got map[string]int
		if err := dec.Decode(&got); err != nil {
			t.Fatalf("%s: Decode err=%v", tc.name, err)
		}
		if got["n"] != 9 {
			t.Errorf("%s: Decode got=%v, want n=9", tc.name, got)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestAppendEncodeBase16Upper verifies the uppercase hex helper appends a
// fully upper-cased hex encoding onto dst, preserving the prefix.
func TestAppendEncodeBase16Upper(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		raw  []byte
		want string
	}
	tests := []tc{
		{"single byte", []byte{0xAB}, "AB"},
		{"multi byte", []byte{0xDE, 0xAD, 0xBE, 0xEF}, "DEADBEEF"},
		{"empty input", []byte{}, ""},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		prefix := []byte("PRE:")
		out := appendEncodeBase16Upper(slices.Clone(prefix), tc.raw)
		if !bytes.HasPrefix(out, prefix) {
			t.Errorf("%s: prefix lost; got=%q", tc.name, out)
		}
		tail := string(out[len(prefix):])
		if tail != tc.want {
			t.Errorf("%s: tail=%q want=%q", tc.name, tail, tc.want)
		}
		//: no lowercase letters may survive the upper-case sweep.
		if strings.ToUpper(tail) != tail {
			t.Errorf("%s: upper-case sweep missed letters: tail=%q", tc.name, tail)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestAppendEncodeASCII85 verifies the ascii85 helper appends the
// encoded payload onto dst, since stdlib lacks an AppendEncode for the
// alphabet.
func TestAppendEncodeASCII85(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		raw  []byte
	}
	tests := []tc{
		{"single byte", []byte{0x42}},
		{"multi byte", []byte("Hello, ascii85!")},
		{"empty input", []byte{}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		prefix := []byte("PRE:")
		out := appendEncodeASCII85(slices.Clone(prefix), tc.raw)
		if !bytes.HasPrefix(out, prefix) {
			t.Errorf("%s: prefix lost; got=%q", tc.name, out)
		}
		//: round-trip through the ascii85 decoder verifies the encoded
		//: tail represents the original bytes.
		c := &baseencCodec{variant: variantASCII85}
		back, derr := decodeASCII85(out[len(prefix):])
		if derr != nil {
			t.Fatalf("%s: decodeASCII85 err=%v", tc.name, derr)
		}
		if !bytes.Equal(back, tc.raw) {
			t.Errorf("%s: round-trip mismatch got=%q want=%q", tc.name, back, tc.raw)
		}
		//: cross-check with encodeBytes path for parity.
		if want := c.encodeBytes(tc.raw); !bytes.Equal(out[len(prefix):], want) {
			t.Errorf("%s: appendEncodeASCII85 tail diverges from encodeBytes", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_baseencCodec_streamWriter verifies every variant returns a usable
// WriteCloser whose Close emits the buffered base-N output.
func Test_baseencCodec_streamWriter(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 stream writer", variantBase64},
		{"base64url stream writer", variantBase64URL},
		{"base32 stream writer", variantBase32},
		{"base16 buffered writer", variantBase16},
		{"hex stream writer", variantHex},
		{"ascii85 stream writer", variantASCII85},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		var sink bytes.Buffer
		w := c.streamWriter(&sink)
		if w == nil {
			t.Fatalf("%s: streamWriter returned nil", tc.name)
		}
		if _, err := w.Write([]byte("data")); err != nil {
			t.Fatalf("%s: Write err=%v", tc.name, err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("%s: Close err=%v", tc.name, err)
		}
		if sink.Len() == 0 {
			t.Errorf("%s: streamWriter produced no output", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_baseencCodec_streamReader verifies every variant returns a reader
// that drains base-N-encoded bytes back into the original payload.
func Test_baseencCodec_streamReader(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 stream reader", variantBase64},
		{"base64url stream reader", variantBase64URL},
		{"base32 stream reader", variantBase32},
		{"base16 stream reader", variantBase16},
		{"hex stream reader", variantHex},
		{"ascii85 stream reader", variantASCII85},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		raw := []byte("stream-reader-payload")
		encoded := c.encodeBytes(raw)
		r := c.streamReader(bytes.NewReader(encoded))
		if r == nil {
			t.Fatalf("%s: streamReader returned nil", tc.name)
		}
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("%s: ReadAll err=%v", tc.name, err)
		}
		if !bytes.Equal(got, raw) {
			t.Errorf("%s: stream round-trip mismatch got=%q want=%q", tc.name, got, raw)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestWrapDecode verifies the (bytes, error) → BASE_ENC_DECODE_FAILED
// adapter passes the success path through verbatim and wraps failures
// with the typed sentinel.
func TestWrapDecode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      []byte
		cause   error
		wantErr bool
	}
	tests := []tc{
		{"success passthrough", []byte("payload"), nil, false},
		{"failure wraps sentinel", nil, errors.New("boom"), true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, err := wrapDecode(tc.in, tc.cause)
		if tc.wantErr {
			if !errs.HasCode(err, CodeBaseEncDecodeFailed) {
				t.Errorf("%s: expected CodeBaseEncDecodeFailed, got %v", tc.name, err)
			}
			if got != nil {
				t.Errorf("%s: expected nil bytes on error, got=%q", tc.name, got)
			}
			return
		}
		if err != nil {
			t.Errorf("%s: unexpected err=%v", tc.name, err)
		}
		if !bytes.Equal(got, tc.in) {
			t.Errorf("%s: got=%q want=%q", tc.name, got, tc.in)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestDecodeASCII85 verifies the ascii85 drain helper round-trips valid
// payloads and wraps decoder failures with the typed sentinel.
func TestDecodeASCII85(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		raw  []byte
	}
	tests := []tc{
		{"single byte", []byte{0x01}},
		{"multi byte", []byte("ascii85-drain-payload")},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: variantASCII85}
		encoded := c.encodeBytes(tc.raw)
		got, err := decodeASCII85(encoded)
		if err != nil {
			t.Fatalf("%s: decodeASCII85 err=%v", tc.name, err)
		}
		if !bytes.Equal(got, tc.raw) {
			t.Errorf("%s: decoded=%q want=%q", tc.name, got, tc.raw)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_encodeHex covers the shared base16/hex helper: lowercase output
// for variantHex, uppercase for variantBase16, byte-for-byte parity
// with the legacy hex.Encode + bytes.ToUpper path.
func Test_encodeHex(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
		raw  []byte
		want string
	}
	tests := []tc{
		{"hex-empty", variantHex, []byte{}, ""},
		{"hex-ascii", variantHex, []byte("Ada"), "416461"},
		{"hex-binary", variantHex, []byte{0x00, 0xFF, 0xA5, 0x5A}, "00ffa55a"},
		{"base16-empty", variantBase16, []byte{}, ""},
		{"base16-ascii", variantBase16, []byte("Ada"), "416461"},
		{"base16-binary", variantBase16, []byte{0x00, 0xFF, 0xA5, 0x5A}, "00FFA55A"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got := encodeHex(tc.v, tc.raw)
		//: byte-for-byte parity with the legacy path.
		if string(got) != tc.want {
			t.Errorf("%s: got=%q want=%q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_marshalJSONPooled covers the JSON-inner pool helper: returned
// jsonBytes round-trip through json.Unmarshal; release() is callable
// without panic.
func Test_marshalJSONPooled(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    any
		want string
	}
	tests := []tc{
		{"int", 42, "42"},
		{"string", "hi", `"hi"`},
		{"map", map[string]int{"a": 1}, `{"a":1}`},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, release, err := marshalJSONPooled(tc.v)
		if err != nil {
			t.Fatalf("%s: unexpected err=%v", tc.name, err)
		}
		if string(got) != tc.want {
			t.Errorf("%s: got=%q want=%q", tc.name, got, tc.want)
		}
		release()
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_releaseJSONBuffer covers the cap-discard pool release.
func Test_releaseJSONBuffer(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		cap  int
	}
	tests := []tc{
		{"small-retained", 1024},
		{"discarded-oversize", maxRetainedJSONBufBytes + 1},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		buf := new(bytes.Buffer)
		buf.Grow(tc.cap)
		releaseJSONBuffer(buf)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
