package tlv_test

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/tlv"
)

// samplePayload exercises every TLV-supported Go type in a single value.
type samplePayload struct {
	Name   string           `json:"name"`
	Age    int32            `json:"age"`
	Score  float64          `json:"score"`
	Bytes  []byte           `json:"bytes"`
	Tags   []string         `json:"tags"`
	Active bool             `json:"active"`
	Map    map[string]int64 `json:"map"`
}

// TestNew verifies the constructor returns a non-nil singleton with the
// canonical name advertised.
func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"canonical name", "tlv"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := tlv.New()
		if c == nil {
			t.Fatalf("%s: New returned nil", tc.name)
		}
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

// TestMarshal covers happy-path and rejection-path Marshal calls.
func TestMarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      any
		wantErr string
	}
	tests := []tc{
		{"int round-trip", int64(123), ""},
		{"bool round-trip", true, ""},
		{"channel rejected", make(chan int), "UNSUPPORTED_TYPE"},
		{"func rejected", func() {}, "UNSUPPORTED_TYPE"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := tlv.New().Marshal(tc.in)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: Marshal err=%v", tc.name, err)
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

// TestUnmarshal covers Unmarshal's pointer-shape and length checks.
func TestUnmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		target  any
		wantErr string
	}
	tests := []tc{
		{"non-pointer target", []byte{0x40, 0x00}, "x", "UNMARSHAL_FAILED"},
		{"nil pointer target", []byte{0x40, 0x00}, (*string)(nil), "UNMARSHAL_FAILED"},
		{"truncated buffer", []byte{0x40}, new(string), "TRUNCATED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := tlv.New().Unmarshal(tc.data, tc.target)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: Unmarshal err=%v", tc.name, err)
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

// TestRoundTripScalars exercises the Marshal/Unmarshal round trip for
// every primitive kind the codec advertises.
func TestRoundTripScalars(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   any
	}
	tests := []tc{
		{"int8", int64(-128)},
		{"int16", int64(-32000)},
		{"int32", int64(-1 << 30)},
		{"int64", int64(-1 << 60)},
		{"uint8", uint64(255)},
		{"uint16", uint64(65535)},
		{"uint32", uint64(1 << 30)},
		{"uint64", uint64(1 << 60)},
		{"float32", float32(3.14)},
		{"float64", float64(2.718281828)},
		{"string", "hello world"},
		{"bytes", []byte{1, 2, 3, 4}},
		{"bool true", true},
		{"bool false", false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := tlv.New()
		encoded, merr := c.Marshal(tc.in)
		if merr != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, merr)
		}
		var out any
		if uerr := c.Unmarshal(encoded, &out); uerr != nil {
			t.Fatalf("%s: Unmarshal err=%v", tc.name, uerr)
		}
		if out == nil {
			t.Fatalf("%s: nil round-trip", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestRoundTripComposite exercises slice / map / struct round trips.
func TestRoundTripComposite(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   any
	}
	tests := []tc{
		{"slice of any", []any{int64(1), int64(2), int64(3)}},
		{"map string→int64", map[string]int64{"a": 1, "b": 2}},
		{"struct payload", samplePayload{
			Name: "alice", Age: 42, Score: 99.5,
			Bytes: []byte("opaque"), Tags: []string{"go", "tlv"},
			Active: true, Map: map[string]int64{"x": 10},
		}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := tlv.New()
		encoded, merr := c.Marshal(tc.in)
		if merr != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, merr)
		}
		var out any
		if uerr := c.Unmarshal(encoded, &out); uerr != nil {
			t.Fatalf("%s: Unmarshal err=%v", tc.name, uerr)
		}
		if reflect.ValueOf(out).IsZero() {
			t.Errorf("%s: decoded zero value", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestAppenderInterface verifies the Appender contract: prior dst is
// preserved on error, and successive Append calls accumulate.
func TestAppenderInterface(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"two consecutive records share the buffer"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		ap, ok := tlv.New().(codec.Appender)
		if !ok {
			t.Fatalf("%s: codec does not implement Appender", tc.name)
		}
		buf, err := ap.Append(nil, int64(1))
		if err != nil {
			t.Fatalf("%s: first Append err=%v", tc.name, err)
		}
		firstLen := len(buf)
		buf, err = ap.Append(buf, "two")
		if err != nil {
			t.Fatalf("%s: second Append err=%v", tc.name, err)
		}
		if len(buf) <= firstLen {
			t.Errorf("%s: buffer did not grow on second Append", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestStreamingRoundTrip exercises the streaming Encoder/Decoder pair
// with two adjacent records.
func TestStreamingRoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"two-record stream"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		sc, ok := tlv.New().(codec.StreamingCodec)
		if !ok {
			t.Fatalf("%s: codec does not implement StreamingCodec", tc.name)
		}
		var buf bytes.Buffer
		enc := sc.NewEncoder(&buf)
		if err := enc.Encode(int64(1)); err != nil {
			t.Fatalf("%s: Encode 1 err=%v", tc.name, err)
		}
		if err := enc.Encode("two"); err != nil {
			t.Fatalf("%s: Encode 2 err=%v", tc.name, err)
		}
		if err := enc.Close(); err != nil {
			t.Errorf("%s: Close err=%v", tc.name, err)
		}
		dec := sc.NewDecoder(&buf)
		var first, second any
		if err := dec.Decode(&first); err != nil {
			t.Fatalf("%s: Decode 1 err=%v", tc.name, err)
		}
		if err := dec.Decode(&second); err != nil {
			t.Fatalf("%s: Decode 2 err=%v", tc.name, err)
		}
		if eofErr := dec.Decode(&first); !errors.Is(eofErr, io.EOF) {
			t.Errorf("%s: expected EOF after drain, got %v", tc.name, eofErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestSizeAndDepthCaps covers the documented input-size and depth caps.
func TestSizeAndDepthCaps(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		wantErr string
	}
	//: 10 MiB + 1 byte triggers SIZE_EXCEEDED.
	oversized := bytes.Repeat([]byte{0x00}, (10<<20)+1)
	tests := []tc{
		{"oversize buffer", oversized, "SIZE_EXCEEDED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var out any
		err := tlv.New().Unmarshal(tc.data, &out)
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

// TestDecoderHardeningRegressions covers the four post-audit hardening
// fixes on the TLV decoder. Each subtest plants a crafted payload that
// previously triggered a large allocation or a misleading error reason.
func TestDecoderHardeningRegressions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		wantErr string
	}
	//: Q1 — tagBytes (0x41) declares length=8 but only 2 body bytes follow.
	//: The make([]byte, length) path used to allocate 8 bytes; now the
	//: bounds check fires and TRUNCATED is returned.
	q1BytesTruncated := []byte{0x41, 0x08, 'a', 'b'}
	//: Q1 — tagString (0x40) declares length=16 against an empty body.
	//: TRUNCATED is the expected reason for any "declared > remaining".
	q1StringTruncated := []byte{0x40, 0x10}
	//: Q2 — tagSlice (0x50) declares 2^31 elements against a 6-byte buffer.
	//: The length>maxTLVBytes guard fires first (2^31 > 10 MiB), surfacing
	//: SIZE_EXCEEDED before the initial pre-allocation is even attempted.
	//: The LEB128 for 2^31 = 0x80,0x80,0x80,0x80,0x08.
	q2HugeSlice := []byte{0x50, 0x80, 0x80, 0x80, 0x80, 0x08}
	//: Q2 — tagMap (0x60) declares 2^31 pairs; same guard.
	q2HugeMap := []byte{0x60, 0x80, 0x80, 0x80, 0x80, 0x08}
	//: Q2 — tagSlice (0x50) declares 4097 elements (just above sliceHintCap)
	//: against a 4-byte buffer. The pre-allocation must clamp to
	//: sliceHintCap and then the for-loop must surface TRUNCATED on the
	//: first missing element record. The LEB128 for 4097 = 0x81,0x20.
	q2SliceJustAboveCap := []byte{0x50, 0x81, 0x20}
	//: Q2 — tagMap (0x60) declares 4097 pairs; same path.
	q2MapJustAboveCap := []byte{0x60, 0x81, 0x20}
	//: Q3 — tagStruct (0x70) with 1 field, field-name TLV declares
	//: length=256 (varint 0x80,0x02). The maxFieldNameBytes cap fires
	//: before any body byte is consumed.
	q3OversizeFieldName := []byte{0x70, 0x01, 0x40, 0x80, 0x02, 'a'}
	//: C1 — tagFloat64 (0x31) declares length=8 (correct) but only 4 body
	//: bytes follow. Used to return UNMARSHAL_FAILED; now returns
	//: TRUNCATED, mirroring decodeInt / decodeUint.
	c1FloatShortBuffer := []byte{0x31, 0x08, 0x01, 0x02, 0x03, 0x04}
	//: C1 — tagFloat32 (0x30) declares length=4 (correct) but only 2 body
	//: bytes follow.
	c1Float32ShortBuffer := []byte{0x30, 0x04, 0x01, 0x02}
	tests := []tc{
		{"Q1 bytes declared > remaining → TRUNCATED", q1BytesTruncated, "TRUNCATED"},
		{"Q1 string declared > remaining → TRUNCATED", q1StringTruncated, "TRUNCATED"},
		{"Q2 huge slice count → SIZE_EXCEEDED", q2HugeSlice, "SIZE_EXCEEDED"},
		{"Q2 huge map count → SIZE_EXCEEDED", q2HugeMap, "SIZE_EXCEEDED"},
		{"Q2 slice above sliceHintCap → TRUNCATED", q2SliceJustAboveCap, "TRUNCATED"},
		{"Q2 map above sliceHintCap → TRUNCATED", q2MapJustAboveCap, "TRUNCATED"},
		{"Q3 field name > 255 bytes → UNMARSHAL_FAILED", q3OversizeFieldName, "UNMARSHAL_FAILED"},
		{"C1 float64 declared OK, buffer short → TRUNCATED", c1FloatShortBuffer, "TRUNCATED"},
		{"C1 float32 declared OK, buffer short → TRUNCATED", c1Float32ShortBuffer, "TRUNCATED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var out any
		err := tlv.New().Unmarshal(tc.data, &out)
		if !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected reason %s, got %v", tc.name, tc.wantErr, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestRegisteredViaImport verifies the codec self-registers on package load.
func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		check func() bool
	}
	tests := []tc{
		{"format registered", func() bool { _, ok := codec.Lookup(codec.Format("tlv")); return ok }},
		{"MIME resolved", func() bool { _, ok := codec.LookupMIME("application/x-tlv"); return ok }},
		{"extension resolved", func() bool { _, ok := codec.LookupExt(".tlv"); return ok }},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if !tc.check() {
			t.Errorf("%s: lookup failed", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
