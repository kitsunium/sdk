package msgpack

import (
	"bytes"
	"io"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_msgpackCodec_Name covers the canonical identifier returned by the codec.
func Test_msgpackCodec_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{
		{"canonical identifier", "msgpack"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &msgpackCodec{}
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

// Test_msgpackCodec_MIMETypes covers the MIME list and its copy semantics.
func Test_msgpackCodec_MIMETypes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical MIME first", "application/msgpack"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &msgpackCodec{}
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

// Test_msgpackCodec_Extensions covers the extension list.
func Test_msgpackCodec_Extensions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical extension first", ".msgpack"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &msgpackCodec{}
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

// Test_msgpackCodec_Marshal exercises the Marshal path with a single canonical case.
func Test_msgpackCodec_Marshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"canonical marshal"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &msgpackCodec{}
		if _, err := c.Marshal(map[string]int{"a": 1}); err != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_msgpackCodec_Unmarshal exercises the Unmarshal path.
func Test_msgpackCodec_Unmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		wantErr string
	}
	tests := []tc{
		//: malformed byte 0xc1 (reserved/never-used) drives the library
		//: decode error → UNMARSHAL_FAILED wrap.
		{"malformed-byte-fails", []byte{0xc1}, "UNMARSHAL_FAILED"},
		//: input one byte past maxMsgPackBytes trips the size-cap guard
		//: (lines 116-124) before any decode — the DoS defence branch.
		{"over-cap-rejected", make([]byte, maxMsgPackBytes+1), "UNMARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &msgpackCodec{}
		var out map[string]int
		err := c.Unmarshal(tc.data, &out)
		//: every case here expects a failure — assert the reason matches.
		if !errs.HasReason(err, tc.wantErr) {
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

// Test_msgpackCodec_NewEncoder covers the streaming encoder constructor.
func Test_msgpackCodec_NewEncoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"returns non-nil encoder"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &msgpackCodec{}
		if enc := c.NewEncoder(&bytes.Buffer{}); enc == nil {
			t.Errorf("%s: NewEncoder returned nil", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_msgpackCodec_NewDecoder covers the streaming decoder constructor.
func Test_msgpackCodec_NewDecoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"returns non-nil decoder"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &msgpackCodec{}
		if dec := c.NewDecoder(bytes.NewReader(nil)); dec == nil {
			t.Errorf("%s: NewDecoder returned nil", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// countingReader counts the bytes pulled from the underlying reader so the
// streaming-cap test can assert NewDecoder never consumes past the limit.
type countingReader struct {
	inner io.Reader
	//: running total of bytes delivered to the decoder.
	read int
}

// Read records the byte count then delegates to the inner reader.
func (c *countingReader) Read(p []byte) (int, error) {
	//: delegate to the wrapped reader.
	n, err := c.inner.Read(p)
	//: accumulate so the test can check the cap was honoured.
	c.read += n
	//: forward the inner result untouched.
	return n, err
}

// oversizedBinStream returns a MessagePack bin32 value (header 0xc6) whose
// declared payload length is body bytes, followed by exactly that many filler
// bytes. The vendor reads the bin body incrementally up to the declared
// length; sizing body past maxMsgPackBytes lets the test assert the streaming
// cap. The bin (not map32) header is used deliberately: the vendor clamps bin
// growth to bytesAllocLimit, so the unhardened path reads the whole oversized
// body without the catastrophic map pre-alloc — keeping the regression test
// memory-safe while still proving the byte cap is (un)enforced.
func oversizedBinStream(body int) []byte {
	//: 0xc6 = bin32, then a 32-bit big-endian payload length.
	out := []byte{
		0xc6,
		byte(body >> 24), byte(body >> 16), byte(body >> 8), byte(body),
	}
	//: append exactly body filler bytes so the declared length is satisfiable.
	out = append(out, make([]byte, body)...)
	//: hand back the crafted oversized stream.
	return out
}

// Test_msgpackCodec_NewDecoder_StreamingDoS is the V54/V109 regression: the
// streaming NewDecoder must inherit the maxMsgPackBytes cap Unmarshal enforces
// so an oversized value cannot drive unbounded reads/allocation (CWE-400 /
// CWE-1284). Before the io.LimitReader fix the decoder pulled the entire
// oversized body from the reader; after it, the read is bounded to
// maxMsgPackBytes+1 and the truncated decode surfaces as UNMARSHAL_FAILED.
func Test_msgpackCodec_NewDecoder_StreamingDoS(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		body    int
		wantErr string
	}
	tests := []tc{
		//: body one KiB past the cap so a correct LimitReader must truncate.
		{"oversized-declared-bin", maxMsgPackBytes + 1024, "UNMARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &msgpackCodec{}
		cr := &countingReader{inner: bytes.NewReader(oversizedBinStream(tc.body))}
		dec := c.NewDecoder(cr)
		var out any
		err := dec.Decode(&out)
		//: the truncated oversized stream must surface as a typed failure.
		if !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
		//: the decoder must never pull more than the byte cap (+1) — this is
		//: the assertion that fails on the unhardened raw-reader path.
		if cr.read > maxMsgPackBytes+1 {
			t.Errorf("%s: consumed %d bytes, exceeds cap %d", tc.name, cr.read, maxMsgPackBytes+1)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_msgpackCodec_Append covers the Appender extension.
func Test_msgpackCodec_Append(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		dst     []byte
		v       any
		wantErr bool
	}
	tests := []tc{
		{"happy-empty-dst", nil, map[string]int{"a": 1}, false},
		{"happy-prefix-dst", []byte("PRE"), "hello", false},
		{"reject-channel", nil, make(chan int), true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := New()
		ap, ok := c.(interface {
			Append([]byte, any) ([]byte, error)
		})
		if !ok {
			t.Fatalf("%s: msgpackCodec does not implement Appender", tc.name)
		}
		got, err := ap.Append(tc.dst, tc.v)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: want error, got nil", tc.name)
			}
			if len(got) != len(tc.dst) {
				t.Errorf("%s: dst len changed on error", tc.name)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: unexpected err=%v", tc.name, err)
		}
		if len(tc.dst) > 0 && string(got[:len(tc.dst)]) != string(tc.dst) {
			t.Errorf("%s: prefix lost", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
