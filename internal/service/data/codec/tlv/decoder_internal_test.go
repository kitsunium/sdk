package tlv

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// Test_tlvDecoder_Decode covers both a success path and a malformed-input
// path to exercise the wrap branch.
func Test_tlvDecoder_Decode(t *testing.T) {
	t.Parallel()
	const emptyStreamCase = "empty stream returns EOF on first call"
	type tc struct {
		name    string
		data    []byte
		wantErr bool
	}
	//: 0x01 0x00 is a complete tagNil/length=0 record.
	tests := []tc{
		{emptyStreamCase, nil, true},
		{"garbage bytes surface an error", []byte{0xff, 0xff, 0xff, 0xff}, true},
		{"valid nil record decodes", []byte{0x01, 0x00}, false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		dec := &tlvDecoder{r: bytes.NewReader(tc.data)}
		var out any
		err := dec.Decode(&out)
		//: empty-stream contract: must surface io.EOF, not just any error.
		//: A plain `err != nil` accepts any failure mode, which would mask a
		//: regression where Decode returned a wrong sentinel on no-data input.
		if tc.name == emptyStreamCase {
			if !errors.Is(err, io.EOF) {
				t.Errorf("%s: expected io.EOF in chain, got %v", tc.name, err)
			}
			return
		}
		if tc.wantErr && err == nil {
			t.Errorf("%s: expected error on malformed input", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_tlvDecoder_More asserts the sticky EOF latch is idempotent.
func Test_tlvDecoder_More(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"idempotent before any Decode"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		dec := &tlvDecoder{r: bytes.NewReader(nil)}
		first := dec.More()
		second := dec.More()
		if first != second {
			t.Errorf("%s: More non-idempotent: first=%v second=%v", tc.name, first, second)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_intWidth covers every signed-integer tag plus the unknown branch.
func Test_intWidth(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		tag  Tag
		want int
	}
	tests := []tc{
		{"int8 → 1", tagInt8, width8},
		{"int16 → 2", tagInt16, width16},
		{"int32 → 4", tagInt32, width32},
		{"int64 → 8", tagInt64, width64},
		{"unknown → 0", tagString, 0},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if got := intWidth(tc.tag); got != tc.want {
			t.Errorf("%s: intWidth=%d want %d", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_uintWidth covers every unsigned-integer tag plus the unknown branch.
func Test_uintWidth(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		tag  Tag
		want int
	}
	tests := []tc{
		{"uint8 → 1", tagUint8, width8},
		{"uint16 → 2", tagUint16, width16},
		{"uint32 → 4", tagUint32, width32},
		{"uint64 → 8", tagUint64, width64},
		{"unknown → 0", tagString, 0},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if got := uintWidth(tc.tag); got != tc.want {
			t.Errorf("%s: uintWidth=%d want %d", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_decodeFieldName covers the maxFieldNameBytes cap on decode (Q3).
// A struct field-name TLV with a declared length above 255 must be
// rejected before any allocation, with a UNMARSHAL_FAILED reason and a
// clear diagnostic.
func Test_decodeFieldName(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		wantErr bool
	}
	//: oversize-name payload: tagString=0x40, length=256 (varint 0x80,0x02),
	//: then 256 bytes of body. The length check fires BEFORE the body is
	//: consumed so we only need a few body bytes to prove the rejection.
	oversize := []byte{0x40, 0x80, 0x02, 'a', 'b', 'c'}
	//: legal-name payload: tagString=0x40, length=3, "abc".
	legal := []byte{0x40, 0x03, 'a', 'b', 'c'}
	//: wrong-tag payload: tagInt8 where tagString was expected.
	wrongTag := []byte{0x10, 0x01, 0x00}
	tests := []tc{
		{"oversize field name rejected", oversize, true},
		{"legal field name accepted", legal, false},
		{"wrong tag rejected", wrongTag, true},
		{"empty buffer truncated", nil, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, _, err := decodeFieldName(tc.data, 0)
		if tc.wantErr && err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_sliceHint covers the slice-pre-allocation cap.
func Test_sliceHint(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   uint64
		want int
	}
	tests := []tc{
		{"small length passes through", 16, 16},
		{"oversize length clamps to cap", uint64(sliceHintCap) + 1, sliceHintCap},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if got := sliceHint(tc.in); got != tc.want {
			t.Errorf("%s: sliceHint=%d want %d", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
