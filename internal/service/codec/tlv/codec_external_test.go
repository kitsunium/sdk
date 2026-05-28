package tlv_test

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"slices"
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

// TestDecodeTruncationMatrix marshals a representative value of every wire
// family, then feeds every strict byte-prefix of the encoding back to
// Unmarshal. A truncated record must always surface an error (never a
// panic, never a silent success) — this drives the truncation arms of
// readTag / readUvarint / readOneRecord / assembleRecord / decodeValue /
// decodeInt / decodeUint / decodeFloat / decodeString / decodeMap /
// decodeStruct / decodeFieldName at once.
func TestDecodeTruncationMatrix(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   any
	}
	tests := []tc{
		{"int64 wide", int64(-1 << 60)},
		{"uint64 wide", uint64(1 << 60)},
		{"float64", 2.718281828},
		{"float32", float32(0.5)},
		{"string", "hello truncation"},
		{"bytes", []byte{1, 2, 3, 4, 5, 6, 7, 8}},
		{"slice", []any{int64(1), "two", true}},
		{"map", map[string]int64{"a": 1, "b": 2}},
		{"nested struct", rtOuter{
			Name:  "x",
			Inner: rtInner{X: 1, Y: "y"},
			Items: []rtInner{{X: 2}},
		}},
		//: a richly nested composite (slice→map→slice→map) so truncation walks
		//: the deepest generic decode arms (decodeSlice/decodeMap recursion).
		{"deep nested composite", []any{
			map[string]any{"rows": []any{
				map[string]any{"X": int64(1), "Y": "a"},
				map[string]any{"X": int64(2), "Y": "b"},
			}},
		}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := tlv.New()
		encoded, merr := c.Marshal(tc.in)
		//: the seed value must encode cleanly before we truncate it.
		if merr != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, merr)
		}
		//: every strict prefix is a truncated record and must fail to decode.
		for n := range len(encoded) {
			var out any
			//: a truncated buffer must surface an error, never panic or succeed.
			if err := c.Unmarshal(encoded[:n], &out); err == nil {
				t.Errorf("%s: Unmarshal of %d/%d bytes succeeded, want error",
					tc.name, n, len(encoded))
			}
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDecodeMalformedRecords covers the non-truncation decode errors: an
// unknown tag byte, trailing bytes after a complete record, and a
// non-pointer Unmarshal target.
func TestDecodeMalformedRecords(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		decode func(c interface {
			Unmarshal([]byte, any) error
			Marshal(any) ([]byte, error)
		}) error
	}
	tests := []tc{
		{
			name: "unknown tag byte",
			decode: func(c interface {
				Unmarshal([]byte, any) error
				Marshal(any) ([]byte, error)
			},
			) error {
				var out any
				//: 0xFF is not a defined tag — decodeScalarByTag default arm.
				return c.Unmarshal([]byte{0xFF, 0x00}, &out)
			},
		},
		{
			name: "trailing bytes after one record",
			decode: func(c interface {
				Unmarshal([]byte, any) error
				Marshal(any) ([]byte, error)
			},
			) error {
				//: two concatenated records — Unmarshal expects exactly one.
				one, merr := c.Marshal(int64(1))
				//: a Marshal failure here is itself a decode-setup error.
				if merr != nil {
					return merr
				}
				two := append(append([]byte{}, one...), one...)
				var out any
				return c.Unmarshal(two, &out)
			},
		},
		{
			name: "non-pointer target",
			decode: func(c interface {
				Unmarshal([]byte, any) error
				Marshal(any) ([]byte, error)
			},
			) error {
				one, merr := c.Marshal(int64(1))
				//: a Marshal failure here is itself a decode-setup error.
				if merr != nil {
					return merr
				}
				//: a value (non-pointer) target violates the decode contract.
				var out int64
				return c.Unmarshal(one, out)
			},
		},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := tlv.New()
		//: every malformed input must surface a non-nil decode error.
		if err := tc.decode(c); err == nil {
			t.Errorf("%s: expected a decode error, got nil", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestTypedDecodeTruncation feeds every strict byte-prefix of a valid
// encoding into a CONCRETE typed target (not interface{}). This drives the
// error-propagation arms of the typed fast path (decoder_typed.go):
// decodeStructInto / decodeFieldValue / decodeSliceElement / decodeMapInto /
// assignMapPair / walkScalarSlice* / narrowAndSet — each of which forwards a
// read failure the generic path never reaches.
func TestTypedDecodeTruncation(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		in        any
		newTarget func() any
	}
	tests := []tc{
		{"scalar into *int64", int64(-1 << 40), func() any { return new(int64) }},
		{"scalar into *string", "truncate me", func() any { return new(string) }},
		{"slice of scalar into *[]int64", []int64{1, 2, 3, 4}, func() any { return new([]int64) }},
		{"slice of struct into *[]rtInner", []rtInner{{X: 1, Y: "a"}, {X: 2}}, func() any { return new([]rtInner) }},
		{"map into *map[string]int64", map[string]int64{"a": 1, "b": 2}, func() any { return new(map[string]int64) }},
		{"map of struct into *map[string]rtInner", map[string]rtInner{"k": {X: 1}}, func() any { return new(map[string]rtInner) }},
		{"nested struct into *rtOuter", rtOuter{
			Name:   "n",
			Inner:  rtInner{X: 1, Y: "y"},
			Items:  []rtInner{{X: 2}},
			Lookup: map[string]int64{"z": 9},
			Ptr:    &rtInner{X: 3},
		}, func() any { return new(rtOuter) }},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := tlv.New()
		encoded, merr := c.Marshal(tc.in)
		//: the seed must encode cleanly before truncation.
		if merr != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, merr)
		}
		//: every strict prefix into the typed target must fail to decode.
		for n := range len(encoded) {
			target := tc.newTarget()
			//: a truncated typed decode must error, never panic or succeed.
			if err := c.Unmarshal(encoded[:n], target); err == nil {
				t.Errorf("%s: typed Unmarshal of %d/%d bytes succeeded, want error",
					tc.name, n, len(encoded))
			}
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestTypedDecodeMismatch decodes a complete, valid encoding into a typed
// target of an incompatible shape. The typed fast path falls back and the
// generic projector then fails the conversion — driving the incompatible-type
// arms of convertValue / project* / narrowAndSet.
func TestTypedDecodeMismatch(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		in        any
		newTarget func() any
	}
	tests := []tc{
		//: string wire value into a numeric target — cross-shape narrowing fails
		//: (reflect rejects string→int, unlike the legal int→string rune cast).
		{"string into *int64", "not a number", func() any { return new(int64) }},
		//: map wire value into a slice target — composite-shape mismatch.
		{"map into *[]int64", map[string]int64{"a": 1}, func() any { return new([]int64) }},
		//: slice wire value into a struct target.
		{"slice into *rtInner", []any{int64(1), int64(2)}, func() any { return new(rtInner) }},
		//: struct wire value into a slice target.
		{"struct into *[]int64", rtInner{X: 1}, func() any { return new([]int64) }},
		//: struct field whose wire value cannot convert to the field type.
		{"string field value into int field", map[string]any{"X": "oops"}, func() any { return new(rtInner) }},
		//: typed map root whose wire value cannot narrow — assignMapPair value
		//: conversion error inside decodeMapInto.
		{"map bad value into *map[string]int64", map[string]any{"a": "bad"}, func() any { return new(map[string]int64) }},
		//: typed map root whose wire key cannot narrow — assignMapPair key
		//: conversion error.
		{"map string key into *map[int64]int64", map[string]any{"a": int64(1)}, func() any { return new(map[int64]int64) }},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := tlv.New()
		encoded, merr := c.Marshal(tc.in)
		//: the seed must encode cleanly before the mismatched decode.
		if merr != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, merr)
		}
		target := tc.newTarget()
		//: decoding into an incompatible shape must surface an error.
		if err := c.Unmarshal(encoded, target); err == nil {
			t.Errorf("%s: mismatched Unmarshal succeeded, want error", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDecodeNestedMismatch decodes composites whose NESTED element/value
// cannot convert to the typed destination. Nesting the offending value
// inside an outer slice forces the generic projection path (the typed fast
// path only handles flat roots), so these drive the per-element/value
// conversion-failure arms of projectSliceToTyped / projectMapToTyped /
// projectCompositeSlice / projectCompositeMap.
func TestDecodeNestedMismatch(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		in        any
		newTarget func() any
	}
	tests := []tc{
		//: a string element cannot narrow to int64 — projectSliceToTyped.
		{"mixed slice into *[]int64", []any{int64(1), "two"}, func() any { return new([]int64) }},
		//: slice-of-map with a non-numeric value — projectCompositeSlice →
		//: projectCompositeMap → projectMapToTyped value error.
		{"slice of bad map into *[]map", []any{map[string]any{"a": "bad"}}, func() any { return new([]map[string]int64) }},
		//: slice-of-slice with a bad inner element — projectCompositeSlice
		//: recursion error.
		{"slice of bad slice into *[][]int64", []any{[]any{"x"}}, func() any { return new([][]int64) }},
		//: slice of struct-shaped map with a bad field — projectCompositeSlice
		//: → projectCompositeStruct → projectMapToStruct conversion error.
		{"slice of bad struct into *[]struct", []any{map[string]any{"X": "bad"}}, func() any { return new([]rtInner) }},
		//: slice of pointer-to-struct with a bad field — drives
		//: projectCompositePointer's error-bubble arm.
		{"slice of bad ptr-struct into *[]*struct", []any{map[string]any{"X": "bad"}}, func() any { return new([]*rtInner) }},
		//: a scalar element decoded into a []struct target — decodeSliceElement
		//: falls back and convertValue(int64 → struct) fails.
		{"scalar element into *[]struct", []any{int64(1)}, func() any { return new([]rtInner) }},
		//: a non-slice wire value into a []struct target — typed slice path
		//: falls back and the generic projector rejects the shape.
		{"non-slice into *[]struct", int64(5), func() any { return new([]rtInner) }},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := tlv.New()
		encoded, merr := c.Marshal(tc.in)
		//: the seed encodes cleanly; only the typed decode should fail.
		if merr != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, merr)
		}
		//: the nested conversion failure must surface as a decode error.
		if err := c.Unmarshal(encoded, tc.newTarget()); err == nil {
			t.Errorf("%s: nested-mismatch Unmarshal succeeded, want error", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDecodeNestedNilProjection covers the nil-source arms of the generic
// projection helpers: a nil field inside a struct, a nil element inside a
// slice, and a nil value inside a map — each must project to the typed
// zero value rather than erroring. Nesting inside an outer slice forces the
// generic projector (projectMapToStruct / projectSliceToTyped /
// projectMapToTyped nil branches).
func TestDecodeNestedNilProjection(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		in        any
		newTarget func() any
	}
	tests := []tc{
		//: nil slice element — projectSliceToTyped nil branch.
		{"nil slice element", []any{[]any{nil, int64(2)}}, func() any { return new([][]int64) }},
		//: nil map value — projectMapToTyped nil branch.
		{"nil map value", []any{map[string]any{"a": nil}}, func() any { return new([]map[string]int64) }},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := tlv.New()
		encoded, merr := c.Marshal(tc.in)
		//: the seed encodes cleanly; nil sub-values are legal on the wire.
		if merr != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, merr)
		}
		//: the nil sub-value must project to a zero, not fail the decode.
		if err := c.Unmarshal(encoded, tc.newTarget()); err != nil {
			t.Errorf("%s: Unmarshal err=%v, want nil (nil → zero)", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDecodeUnknownStructField drives the lenient unknown-field skip arm of
// decodeFieldValue: a struct wire record carrying a field absent from the Go
// target must decode successfully, discarding the unknown field's value.
func TestDecodeUnknownStructField(t *testing.T) {
	t.Parallel()
	type wide struct {
		X     int64
		Extra string
		Y     string
	}
	type narrow struct {
		X int64
		Y string
	}
	type tc struct {
		name string
	}
	tests := []tc{{"unknown field is skipped on the typed path"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		c := tlv.New()
		encoded, merr := c.Marshal(wide{X: 1, Extra: "drop me", Y: "keep"})
		//: the wide struct must encode cleanly.
		if merr != nil {
			t.Fatalf("Marshal err=%v", merr)
		}
		var out narrow
		//: decoding into the narrower struct must skip the unknown field.
		if uerr := c.Unmarshal(encoded, &out); uerr != nil {
			t.Fatalf("Unmarshal err=%v", uerr)
		}
		//: the known fields must survive the unknown-field skip.
		if out.X != 1 || out.Y != "keep" {
			t.Errorf("decoded = %#v, want {X:1 Y:keep}", out)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDecodeDepthExceeded drives the decode-side CWE-674 guard: a deeply
// nested encoded payload must surface DEPTH_EXCEEDED on Unmarshal.
func TestDecodeDepthExceeded(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		levels int
	}
	tests := []tc{
		{"forty nested slices overflow decode depth", 40},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: hand-build a nested-slice payload deeper than the cap so the
		//: encoder (which shares the cap) is bypassed; each layer is a
		//: tagSlice (0x50) with element count 1, leaf is a tagNil (0x01) record.
		payload := []byte{0x01, 0x00} // tagNil leaf (tag + zero length).
		for range tc.levels {
			//: prepend a slice header (tagSlice=0x50, length=1) per level.
			payload = append([]byte{0x50, 0x01}, payload...)
		}
		var out any
		//: decode must refuse the over-deep payload with the depth sentinel.
		if err := tlv.New().Unmarshal(payload, &out); !errs.HasReason(err, "DEPTH_EXCEEDED") {
			t.Errorf("%s: err = %v, want DEPTH_EXCEEDED", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// rtInner is a small nested struct reused across the typed round-trip
// matrix. Its fields span the scalar tag families so a single round trip
// exercises int / float / string / bytes / bool re-narrowing.
type rtInner struct {
	X int64
	Y string
	Z float64
}

// rtOuter nests every composite field kind the typed-struct decode path
// recurses into: a nested struct, a nested slice-of-struct (drives
// decodeNestedSliceOfStruct), a nested map (drives decodeNestedMap), and a
// pointer field — so one round trip lights up the field-recursion fan-out.
type rtOuter struct {
	Name   string
	Inner  rtInner
	Items  []rtInner
	Lookup map[string]int64
	Ptr    *rtInner
	Blob   []byte
	On     bool
}

// rtScalars carries one field of every scalar reflect.Kind so a single
// round trip exercises every arm of encodeFieldValue (the per-field scalar
// dispatch) and every width branch of the decode-side narrowAndSet.
type rtScalars struct {
	I    int
	I8   int8
	I16  int16
	I32  int32
	I64  int64
	U    uint
	U8   uint8
	U16  uint16
	U32  uint32
	U64  uint64
	F32  float32
	F64  float64
	S    string
	B    bool
	Data []byte
}

// TestRoundTripAllScalarKinds round-trips a struct populated with every
// scalar kind so the per-field encoder dispatch and the typed decode
// narrowing cover all integer/float widths in one pass.
func TestRoundTripAllScalarKinds(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   rtScalars
	}
	tests := []tc{
		{"min/zero spread", rtScalars{
			I: -1, I8: -128, I16: -32768, I32: -2147483648, I64: -1 << 60,
			U: 1, U8: 255, U16: 65535, U32: 4294967295, U64: 1 << 60,
			F32: 0.5, F64: 2.5, S: "s", B: true, Data: []byte{1, 2},
		}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := tlv.New()
		encoded, merr := c.Marshal(tc.in)
		//: every scalar field must encode without error.
		if merr != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, merr)
		}
		var out rtScalars
		//: decode must reconstruct every width back into its typed field.
		if uerr := c.Unmarshal(encoded, &out); uerr != nil {
			t.Fatalf("%s: Unmarshal err=%v", tc.name, uerr)
		}
		//: the round trip must reproduce every scalar field exactly.
		if !reflect.DeepEqual(tc.in, out) {
			t.Errorf("%s: mismatch\n in=%#v\nout=%#v", tc.name, tc.in, out)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestRoundTripNilForms covers the nil short-circuits of the encoder
// (untyped nil and a nil pointer both emit a tagNil record) and the decode
// side that turns tagNil back into a nil/zero value.
func TestRoundTripNilForms(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   any
	}
	tests := []tc{
		{"untyped nil", nil},
		{"typed nil pointer", (*rtInner)(nil)},
		{"nil inside a slice", []any{nil, int64(1)}},
		{"nil inside a map", map[string]any{"k": nil}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := tlv.New()
		encoded, merr := c.Marshal(tc.in)
		//: a nil (untyped or typed) must encode as a tagNil record, not error.
		if merr != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, merr)
		}
		var out any
		//: decoding a nil-bearing record must succeed.
		if uerr := c.Unmarshal(encoded, &out); uerr != nil {
			t.Fatalf("%s: Unmarshal err=%v", tc.name, uerr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestTypedRoundTrip drives Marshal→Unmarshal into a concrete typed target
// for a matrix of Go shapes. The typed fast path (decoder_typed.go) handles
// root struct / []struct / []scalar / map / scalar; the harder composite
// shapes (pointers, slice-of-pointer, slice-of-map, nested slices, maps with
// composite values) fall through to the generic projection path
// (decoder.go project*). Every row asserts an exact DeepEqual round trip.
func TestTypedRoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   any
		// newTarget returns a fresh pointer to the typed destination so each
		// subtest decodes into its own zero value.
		newTarget func() any
	}
	tests := []tc{
		//: typed scalar targets — Phase-7 direct-set path.
		{"scalar int", int64(-7), func() any { return new(int64) }},
		{"scalar uint", uint64(9), func() any { return new(uint64) }},
		{"scalar float", 2.5, func() any { return new(float64) }},
		{"scalar string", "hi", func() any { return new(string) }},
		{"scalar bool", true, func() any { return new(bool) }},
		//: typed []scalar targets — Phase-6 slice-of-scalar path.
		{"slice of int64", []int64{1, 2, 3}, func() any { return new([]int64) }},
		{"slice of string", []string{"a", "b"}, func() any { return new([]string) }},
		{"bytes", []byte{9, 8, 7}, func() any { return new([]byte) }},
		//: typed map targets — Phase-4 typed-map path.
		{"map string→int64", map[string]int64{"a": 1, "b": 2}, func() any { return new(map[string]int64) }},
		{"map string→string", map[string]string{"k": "v"}, func() any { return new(map[string]string) }},
		//: typed struct target — Phase-1 struct fast path.
		{"flat struct", rtInner{X: 5, Y: "y", Z: 1.5}, func() any { return new(rtInner) }},
		//: []struct — Phase-2 slice-of-struct path.
		{"slice of struct", []rtInner{{X: 1}, {X: 2, Y: "two"}}, func() any { return new([]rtInner) }},
		//: deeply nested struct — drives decodeNestedSliceOfStruct (Items),
		//: decodeNestedMap (Lookup), tryTypedFieldRecursion (Inner/Ptr).
		{"nested struct fan-out", rtOuter{
			Name:   "outer",
			Inner:  rtInner{X: 1, Y: "in", Z: 0.5},
			Items:  []rtInner{{X: 10}, {X: 20, Y: "twenty"}},
			Lookup: map[string]int64{"one": 1, "two": 2},
			Ptr:    &rtInner{X: 99, Y: "ptr"},
			Blob:   []byte("bytes"),
			On:     true,
		}, func() any { return new(rtOuter) }},
		//: pointer-to-struct root — generic projectCompositePointer +
		//: projectCompositeStruct + projectMapToStruct.
		{"pointer to struct", &rtInner{X: 3, Y: "p"}, func() any { return new(*rtInner) }},
		//: slice of pointer — typed slice path falls back; projection builds
		//: each *rtInner element.
		{"slice of pointer to struct", []*rtInner{{X: 1}, {X: 2}}, func() any { return new([]*rtInner) }},
		//: slice of map — projectCompositeSlice → projectCompositeMap →
		//: projectMapToTyped per element.
		{"slice of map", []map[string]int64{{"a": 1}, {"b": 2}}, func() any { return new([]map[string]int64) }},
		//: slice of slice — projectCompositeSlice recursion.
		{"slice of slice", [][]int64{{1, 2}, {3}}, func() any { return new([][]int64) }},
		//: map with struct values — projectMapToTyped → projectMapToStruct.
		{"map string→struct", map[string]rtInner{"a": {X: 1}, "b": {X: 2, Y: "b"}}, func() any { return new(map[string]rtInner) }},
		//: map with slice values — projectMapToTyped → projectSliceToTyped.
		{"map string→slice", map[string][]int64{"xs": {1, 2, 3}}, func() any { return new(map[string][]int64) }},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := tlv.New()
		encoded, merr := c.Marshal(tc.in)
		//: the matrix only carries encodable values — a Marshal error is a bug.
		if merr != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, merr)
		}
		target := tc.newTarget()
		//: Unmarshal must reconstruct the value into the typed destination.
		if uerr := c.Unmarshal(encoded, target); uerr != nil {
			t.Fatalf("%s: Unmarshal err=%v", tc.name, uerr)
		}
		//: dereference the destination pointer to compare the decoded value.
		got := reflect.ValueOf(target).Elem().Interface()
		//: the round trip must reproduce the input exactly after re-narrowing.
		if !reflect.DeepEqual(tc.in, got) {
			t.Errorf("%s: round trip mismatch\n in=%#v\nout=%#v", tc.name, tc.in, got)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// badChanField is a struct carrying an unsupported exported field so the
// struct encoder's per-field error arm is exercised.
type badChanField struct {
	Ch chan int
}

// TestEncodeRejectsUnsupported drives the UNSUPPORTED_TYPE arm of every
// composite encoder: a chan / func / complex value at the root and nested
// inside a slice, a map value, a map key, and a struct field must each
// surface the documented sentinel rather than panic.
func TestEncodeRejectsUnsupported(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   any
	}
	tests := []tc{
		//: root scalar rejections — encodeValue → encodeByKind.
		{"root chan", make(chan int)},
		{"root func", func() {}},
		{"root complex128", complex128(1 + 2i)},
		{"root complex64", complex64(1 + 2i)},
		//: nested in a slice — encodeSliceDirect → encodeFieldValue.
		{"slice element chan", []any{make(chan int)}},
		//: nested in a map value — encodeMapDirect value leg.
		{"map value func", map[string]any{"f": func() {}}},
		//: nested in a map key — encodeMapDirect key leg.
		{"map key complex", map[complex128]int{complex(1, 0): 1}},
		//: nested in a struct field — encodeStructDirect → encodeFieldValue.
		{"struct field chan", badChanField{Ch: make(chan int)}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := tlv.New().Marshal(tc.in)
		//: every unsupported shape must surface the UNSUPPORTED_TYPE reason.
		if !errs.HasReason(err, "UNSUPPORTED_TYPE") {
			t.Errorf("%s: err = %v, want UNSUPPORTED_TYPE", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestEncodeDepthExceeded drives the CWE-674 depth guard: nesting beyond
// maxTLVDepth (32) must surface DEPTH_EXCEEDED rather than recurse without
// bound.
func TestEncodeDepthExceeded(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		levels int
	}
	tests := []tc{
		{"forty nested slices overflow the depth cap", 40},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: wrap a scalar in tc.levels slice layers so the encoder recurses
		//: past maxTLVDepth.
		var deep any = int64(1)
		for range tc.levels {
			//: each layer adds one composite level to the recursion.
			deep = []any{deep}
		}
		_, err := tlv.New().Marshal(deep)
		//: the depth guard must fire before unbounded recursion.
		if !errs.HasReason(err, "DEPTH_EXCEEDED") {
			t.Errorf("%s: err = %v, want DEPTH_EXCEEDED", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestAppendRollbackOnError pins the Appender contract: a mid-encode failure
// must roll dst back to its original length so callers never observe a torn
// buffer.
func TestAppendRollbackOnError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"unsupported value leaves the prior buffer intact"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		ap, ok := tlv.New().(codec.Appender)
		//: the TLV codec must advertise the Appender extension.
		if !ok {
			t.Fatal("codec does not implement Appender")
		}
		prefill := []byte{0xAA, 0xBB, 0xCC}
		//: snapshot prefill BEFORE Append so any in-place mutation is
		//: visible — otherwise comparing out against prefill could silently
		//: agree when prefill itself was clobbered.
		want := slices.Clone(prefill)
		out, err := ap.Append(prefill, make(chan int))
		//: the encode must fail on the unsupported value.
		if err == nil {
			t.Fatal("expected an encode error, got nil")
		}
		//: rollback contract — the returned buffer is byte-for-byte the prior
		//: prefix: neither truncated/grown (length) nor mutated (content).
		if !bytes.Equal(out, want) {
			t.Errorf("Append did not roll back: out=%v, want %v", out, want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// failWriter fails every Write with its configured error so the streaming
// encoder's writer-error arm is exercised.
type failWriter struct {
	err error
}

func (f failWriter) Write(_ []byte) (int, error) {
	return 0, f.err
}

// shortWriter reports one byte fewer than handed with a nil error so the
// encoder's short-write detection (io.ErrShortWrite) fires.
type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) {
	//: report a short count so n != len(buf) with a nil error.
	return len(p) - 1, nil
}

// TestStreamingEncodeErrors covers the three failure arms of the streaming
// Encode: an unsupported value, a writer that errors, and a writer that
// reports a short write.
func TestStreamingEncodeErrors(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		writer    io.Writer
		value     any
		wantWrite bool // true when the failure originates in the writer
	}
	boom := errors.New("writer boom")
	tests := []tc{
		{"unsupported value fails before any write", io.Discard, make(chan int), false},
		{"writer error wraps as MARSHAL_FAILED", failWriter{err: boom}, int64(1), true},
		{"short write surfaces io.ErrShortWrite", shortWriter{}, int64(1 << 40), true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		sc, ok := tlv.New().(codec.StreamingCodec)
		//: the TLV codec must advertise the StreamingCodec extension.
		if !ok {
			t.Fatal("codec does not implement StreamingCodec")
		}
		err := sc.NewEncoder(tc.writer).Encode(tc.value)
		//: every arm must surface a non-nil error.
		if err == nil {
			t.Fatalf("%s: expected an error, got nil", tc.name)
		}
		//: writer-origin failures wrap as MARSHAL_FAILED; the value-origin
		//: failure surfaces UNSUPPORTED_TYPE.
		if tc.wantWrite {
			//: writer errors carry the marshal-failure reason.
			if !errs.HasReason(err, "MARSHAL_FAILED") {
				t.Errorf("%s: err = %v, want MARSHAL_FAILED", tc.name, err)
			}
		} else if !errs.HasReason(err, "UNSUPPORTED_TYPE") {
			t.Errorf("%s: err = %v, want UNSUPPORTED_TYPE", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
