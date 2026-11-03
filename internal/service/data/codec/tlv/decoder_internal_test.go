package tlv

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
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

// countingReader counts the bytes a decoder pulls from the stream it wraps.
type countingReader struct {
	r io.Reader
	n int
}

// Read forwards to the wrapped reader and adds what it delivered.
func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// fillReader is an endless stream of one byte.
type fillReader byte

// Read fills p with the byte.
func (f fillReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(f)
	}
	return len(p), nil
}

// compositeOfStrings frames a slice record declaring len(sizes) string
// children, the i-th carrying sizes[i] bytes, without materialising them.
func compositeOfStrings(sizes ...int) io.Reader {
	header := binary.AppendUvarint([]byte{byte(tagSlice)}, uint64(len(sizes)))
	parts := []io.Reader{bytes.NewReader(header)}
	for _, size := range sizes {
		child := binary.AppendUvarint([]byte{byte(tagString)}, uint64(size))
		parts = append(parts, bytes.NewReader(child), io.LimitReader(fillReader('a'), int64(size)))
	}
	return io.MultiReader(parts...)
}

// compositeBytes is compositeOfStrings materialised, for the buffered path.
func compositeBytes(t *testing.T, sizes ...int) []byte {
	t.Helper()
	data, err := io.ReadAll(compositeOfStrings(sizes...))
	if err != nil {
		t.Fatalf("framing the composite: %v", err)
	}
	return data
}

// compositeOverhead is the bytes a one-child slice record spends beyond its
// child's payload: the slice tag and count, the child's tag and length.
func compositeOverhead(payload int) int {
	return 1 + len(binary.AppendUvarint(nil, 1)) + 1 + len(binary.AppendUvarint(nil, uint64(payload)))
}

// TestStreamDecodeChargesTheWholeRecordAgainstTheCeiling is #1105: the stream
// Decoder used to cap each scalar child at maxTLVBytes and rebuild a composite
// with no total bound, so four 9 MiB children materialised 36 MiB before
// decodeRoot saw a byte. The record as a whole now obeys Unmarshal's ceiling:
// the refusal comes before the second child is read, and what it allocated
// stays within two ceilings. No t.Parallel: it reads the process-wide
// allocation counter.
func TestStreamDecodeChargesTheWholeRecordAgainstTheCeiling(t *testing.T) {
	const child = 9 << 20
	counted := &countingReader{r: compositeOfStrings(child, child, child, child)}
	dec := &tlvDecoder{r: counted}
	var got any
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err := dec.Decode(&got)
	runtime.ReadMemStats(&after)
	if !errs.HasReason(err, "SIZE_EXCEEDED") {
		t.Fatalf("want SIZE_EXCEEDED, got %v", err)
	}
	if counted.n > maxTLVBytes+1 {
		t.Fatalf("read %d bytes from the stream, want at most %d", counted.n, maxTLVBytes+1)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 2*uint64(maxTLVBytes) {
		t.Fatalf("allocated %d bytes, want at most %d", grew, 2*maxTLVBytes)
	}
	if dec.More() {
		t.Fatal("a refused record must halt the stream")
	}
}

// TestStreamDecodeAgreesWithUnmarshalAtTheCeiling frames one composite whose
// total is one byte under, exactly at and one byte over maxTLVBytes: the stream
// Decoder and Unmarshal accept and refuse the same records.
func TestStreamDecodeAgreesWithUnmarshalAtTheCeiling(t *testing.T) {
	t.Parallel()
	payload := maxTLVBytes - compositeOverhead(maxTLVBytes)
	for _, tc := range []struct {
		name    string
		payload int
		refused bool
	}{
		{"one byte under the ceiling", payload - 1, false},
		{"exactly the ceiling", payload, false},
		{"one byte over the ceiling", payload + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := compositeBytes(t, tc.payload)
			var buffered, streamed any
			uerr := (&tlvCodec{}).Unmarshal(data, &buffered)
			derr := (&tlvDecoder{r: bytes.NewReader(data)}).Decode(&streamed)
			if tc.refused {
				if !errs.HasReason(uerr, "SIZE_EXCEEDED") || !errs.HasReason(derr, "SIZE_EXCEEDED") {
					t.Fatalf("want SIZE_EXCEEDED on both paths, got Unmarshal %v, Decode %v", uerr, derr)
				}
				return
			}
			if uerr != nil || derr != nil {
				t.Fatalf("want both paths to decode, got Unmarshal %v, Decode %v", uerr, derr)
			}
			elems, ok := streamed.([]any)
			if !ok || len(elems) != 1 || len(elems[0].(string)) != tc.payload {
				t.Fatalf("stream decoded %T, want one %d-byte string", streamed, tc.payload)
			}
		})
	}
}
