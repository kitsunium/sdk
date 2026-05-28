package tlv

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// craftedHugeLength is a declared structural length well past maxTLVBytes
// (10 MiB) so the per-record length cap fires while the crafted buffer
// itself stays tiny — the OUTER input-size cap never triggers.
const craftedHugeLength uint64 = 20_000_000

// errReader fails every Read with a non-EOF error so the streaming
// decoder's wrapped-read-failure arms are exercised.
type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, errors.New("read boom")
}

// scriptReader delivers head bytes across reads, then fails with a non-EOF
// error — letting a failure land mid-value (after a valid header) so
// assembleRecord's ReadFrom-error arm is reached.
type scriptReader struct {
	head []byte
	pos  int
}

func (s *scriptReader) Read(p []byte) (int, error) {
	//: deliver the scripted header bytes first.
	if s.pos < len(s.head) {
		n := copy(p, s.head[s.pos:])
		s.pos += n
		return n, nil
	}
	//: header drained — fail mid-value with a non-EOF error.
	return 0, errors.New("mid-value boom")
}

// TestStreamingDecodeErrors drives the streaming reader's failure arms
// (readTag / readUvarint / assembleRecord / readOneRecord / Decode) which
// the byte-slice Unmarshal path never touches: a failing reader, a truncated
// value, a missing/overlong length varint, and an oversize declared length.
func TestStreamingDecodeErrors(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		reader func() io.Reader
	}
	tests := []tc{
		//: reader errors on the tag byte — readTag wrap + Decode bail.
		{"reader fails on tag", func() io.Reader { return errReader{} }},
		//: valid bytes header but the reader fails before the value bytes
		//: arrive — assembleRecord ReadFrom error.
		{"reader fails mid-value", func() io.Reader {
			return &scriptReader{head: appendTagLen(nil, tagBytes, 5)}
		}},
		//: truncated value — declared 5 bytes, only 2 delivered.
		{"short value", func() io.Reader {
			rec := appendTagLen(nil, tagBytes, 5)
			rec = append(rec, 1, 2)
			return bytes.NewReader(rec)
		}},
		//: length varint missing entirely after the tag.
		{"missing length varint", func() io.Reader {
			return bytes.NewReader([]byte{byte(tagInt64)})
		}},
		//: declared length beyond maxTLVBytes — readOneRecord size cap.
		{"oversize declared length", func() io.Reader {
			return bytes.NewReader(appendTagLen(nil, tagBytes, craftedHugeLength))
		}},
		//: ten-byte varint whose final byte exceeds the legal MSB — overflow.
		{"varint overflow terminal byte", func() io.Reader {
			over := []byte{byte(tagBytes), 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x02}
			return bytes.NewReader(over)
		}},
		//: all-continuation varint exhausts the 10-byte budget.
		{"varint exceeds budget", func() io.Reader {
			over := append([]byte{byte(tagBytes)}, bytes.Repeat([]byte{0x80}, maxVarintBytes)...)
			return bytes.NewReader(over)
		}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		sc, ok := New().(codec.StreamingCodec)
		//: the TLV codec must advertise streaming.
		if !ok {
			t.Fatal("codec does not implement StreamingCodec")
		}
		var out any
		//: every malformed stream must surface a non-nil decode error.
		if err := sc.NewDecoder(tc.reader()).Decode(&out); err == nil {
			t.Errorf("%s: streaming Decode succeeded, want error", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// craftInner is a small struct target used by the crafted-byte decode tests
// (this file is white-box, so it cannot reach the tlv_test package types).
type craftInner struct {
	X int64
	Y string
}

// TestDecodeStructuralLengthCaps drives the declared-length-DoS guards
// (CWE-400): a record header advertising more children than maxTLVBytes
// must surface SIZE_EXCEEDED before any pre-allocation, on BOTH the generic
// (interface{}) and the typed fast paths. The crafted buffers carry only the
// header so the structural cap — not the input-size cap — is the gate.
func TestDecodeStructuralLengthCaps(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		data      []byte
		newTarget func() any
	}
	tests := []tc{
		//: generic map header with an absurd pair count.
		{"huge map → any", appendTagLen(nil, tagMap, craftedHugeLength), func() any { return new(any) }},
		//: typed map header — drives tryDecodeRootIntoMap's cap.
		{"huge map → *map", appendTagLen(nil, tagMap, craftedHugeLength), func() any { return new(map[string]int64) }},
		//: generic slice header with an absurd element count.
		{"huge slice → any", appendTagLen(nil, tagSlice, craftedHugeLength), func() any { return new(any) }},
		//: typed slice-of-scalar header — drives the scalar slice cap.
		{"huge slice → *[]int64", appendTagLen(nil, tagSlice, craftedHugeLength), func() any { return new([]int64) }},
		//: typed slice-of-struct header — drives the struct slice cap.
		{"huge slice → *[]struct", appendTagLen(nil, tagSlice, craftedHugeLength), func() any { return new([]craftInner) }},
		//: generic struct header with an absurd field count.
		{"huge struct → any", appendTagLen(nil, tagStruct, craftedHugeLength), func() any { return new(any) }},
		//: typed struct header — drives decodeStructFromHeader's cap.
		{"huge struct → *struct", appendTagLen(nil, tagStruct, craftedHugeLength), func() any { return new(craftInner) }},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: the structural cap must reject the over-large declared length.
		if err := New().Unmarshal(tc.data, tc.newTarget()); !errs.HasReason(err, "SIZE_EXCEEDED") {
			t.Errorf("%s: err = %v, want SIZE_EXCEEDED", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// recSlice is self-referential through a slice field, so a crafted payload
// can nest tagStruct→tagSlice records past maxTLVDepth and drive the typed
// nested-recursion depth guards (decodeStructFromHeader /
// decodeNestedSliceOfStruct / decodeSliceOfStructInto) into a single
// concrete Go type — something the encoder (which shares the cap) cannot
// produce on its own.
type recSlice struct {
	Children []recSlice
}

// recMap is self-referential through a map value, the map-flavoured
// companion to recSlice for the decodeNestedMap / decodeMapInto depth guards.
type recMap struct {
	M map[string]recMap
}

// TestDecodeTypedNestedDepth drives the CWE-674 depth guards on the TYPED
// nested-recursion paths. Crafted payloads nest a self-referential struct
// past maxTLVDepth; because the typed recursion increments depth per level,
// the decoder must surface DEPTH_EXCEEDED rather than recurse without bound.
func TestDecodeTypedNestedDepth(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		build     func(levels int) []byte
		newTarget func() any
	}
	//: levels well past maxTLVDepth (the typed recursion adds ~2 depth/level,
	//: so 40 struct levels guarantees the guard fires).
	const levels int = 40
	tests := []tc{
		{
			name: "nested slice-of-struct depth",
			build: func(levels int) []byte {
				//: innermost empty struct, then wrap in struct{Children:[self]}.
				node := appendTagLen(nil, tagStruct, 0)
				for range levels {
					inner := append(appendTagLen(nil, tagSlice, 1), node...)
					node = append(appendTagLen(nil, tagStruct, 1), structField("Children", inner)...)
				}
				return node
			},
			newTarget: func() any { return new(recSlice) },
		},
		{
			name: "nested map depth",
			build: func(levels int) []byte {
				//: innermost empty struct, then wrap in struct{M:{ "k": self }}.
				node := appendTagLen(nil, tagStruct, 0)
				for range levels {
					key := append(appendTagLen(nil, tagString, 1), 'k')
					mapRec := append(appendTagLen(nil, tagMap, 1), append(key, node...)...)
					node = append(appendTagLen(nil, tagStruct, 1), structField("M", mapRec)...)
				}
				return node
			},
			newTarget: func() any { return new(recMap) },
		},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: the over-deep crafted payload must trip the depth sentinel.
		if err := New().Unmarshal(tc.build(levels), tc.newTarget()); !errs.HasReason(err, "DEPTH_EXCEEDED") {
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

// TestDecodeMalformedScalarLengths drives the malformed-length arms of
// decodeInt / decodeUint / decodeFloat: a fixed-width scalar tag whose
// declared length disagrees with the tag's intrinsic width must be rejected
// (distinct from a truncated payload, which carries the right length).
func TestDecodeMalformedScalarLengths(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		tag  Tag
		// badLen is a length that does not equal the tag's intrinsic width.
		badLen uint64
	}
	tests := []tc{
		{"int8 wrong length", tagInt8, 5},
		{"int16 wrong length", tagInt16, 1},
		{"int32 wrong length", tagInt32, 2},
		{"int64 wrong length", tagInt64, 3},
		{"uint8 wrong length", tagUint8, 2},
		{"uint16 wrong length", tagUint16, 1},
		{"uint32 wrong length", tagUint32, 2},
		{"uint64 wrong length", tagUint64, 3},
		{"float32 wrong length", tagFloat32, 2},
		{"float64 wrong length", tagFloat64, 9},
		//: zero-payload tags (nil/bool) must declare length 0 — a non-zero
		//: length is malformed (decodeScalarByTag zero-payload arm).
		{"nil non-zero length", tagNil, 1},
		{"bool-true non-zero length", tagBoolTrue, 3},
		{"bool-false non-zero length", tagBoolFalse, 2},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: craft a record whose declared length disagrees with the tag width,
		//: padding the body so the length-mismatch (not truncation) is the gate.
		data := appendTagLen(nil, tc.tag, tc.badLen)
		data = append(data, make([]byte, tc.badLen)...)
		var out any
		//: the width/length disagreement must surface a decode error.
		if err := New().Unmarshal(data, &out); err == nil {
			t.Errorf("%s: malformed-length record decoded without error", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// craftFields carries a map field and a slice-of-struct field so the
// nested typed-recursion helpers (decodeNestedMap / decodeNestedSliceOfStruct)
// can be driven with crafted malformed nested records.
type craftFields struct {
	M map[string]int64
	L []craftInner
}

// structField builds one struct field record: a tagString name record
// followed by the caller-supplied value bytes.
func structField(name string, value []byte) []byte {
	out := appendTagLen(nil, tagString, uint64(len(name)))
	out = append(out, name...)
	return append(out, value...)
}

// TestDecodeUnhashableMapKey drives the unhashable-key arm of decodeMap: a
// wire map whose key is itself a composite (slice) cannot be a Go map key, so
// the decoder must reject it rather than panic on the map insert.
func TestDecodeUnhashableMapKey(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"slice key is not hashable"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: a map with one pair whose KEY is an empty slice (tagSlice len 0)
		//: and whose value is a nil record — the slice key is unhashable.
		data := appendTagLen(nil, tagMap, 1)
		data = append(data, appendTagLen(nil, tagSlice, 0)...) // key: []any{}
		data = append(data, appendTagLen(nil, tagNil, 0)...)   // value: nil
		var out any
		//: the unhashable key must surface a decode error, not a panic.
		if err := New().Unmarshal(data, &out); err == nil {
			t.Error("unhashable map key decoded without error")
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDecodeStructFieldValueErrors drives two field-value arms of the typed
// struct decode: a KNOWN field whose wire value cannot convert to the field
// type (assignFieldValue conversion error), and an UNKNOWN field whose value
// record is truncated (decodeFieldValue skip-path decode error).
func TestDecodeStructFieldValueErrors(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		field []byte
	}
	tests := []tc{
		//: known field X (int64) whose wire value is a string — convertValue
		//: rejects string→int64, driving assignFieldValue's error arm.
		{"X: unconvertible string", structField("X", append(appendTagLen(nil, tagString, 3), "bad"...))},
		//: unknown field whose int64 value declares 8 bytes but delivers none —
		//: the skip-path decodeValue fails on the truncated value.
		{"unknown: truncated value", structField("Zzz", appendTagLen(nil, tagInt64, uint64(width64)))},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: wrap the crafted field in a one-field struct record.
		data := appendTagLen(nil, tagStruct, 1)
		data = append(data, tc.field...)
		var out craftInner
		//: both malformations must surface a decode error on the typed path.
		if err := New().Unmarshal(data, &out); err == nil {
			t.Errorf("%s: malformed field value decoded without error", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDecodeNestedFieldMalformed drives the size-cap and truncated-length
// arms of decodeNestedMap and decodeNestedSliceOfStruct: a struct whose map
// or slice-of-struct field carries a malformed nested header must surface a
// decode error on the typed struct path.
func TestDecodeNestedFieldMalformed(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		value []byte // the field value record (after the field name).
	}
	tests := []tc{
		//: map field declaring more pairs than maxTLVBytes — size cap.
		{"M: huge map", appendTagLen(nil, tagMap, craftedHugeLength)},
		//: map field whose length varint is missing — truncated.
		{"M: map missing length", []byte{byte(tagMap)}},
		//: slice-of-struct field declaring too many elements — size cap.
		{"L: huge slice", appendTagLen(nil, tagSlice, craftedHugeLength)},
		//: slice-of-struct field whose length varint is missing — truncated.
		{"L: slice missing length", []byte{byte(tagSlice)}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: build a one-field struct record around the crafted field value.
		fieldName := tc.name[:1]
		data := appendTagLen(nil, tagStruct, 1)
		data = append(data, structField(fieldName, tc.value)...)
		var out craftFields
		//: the malformed nested header must surface a decode error.
		if err := New().Unmarshal(data, &out); err == nil {
			t.Errorf("%s: malformed nested field decoded without error", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDecodeTrailingBytesTyped drives the trailing-bytes arms of the typed
// fast path: a complete, valid record followed by junk bytes must be
// rejected by tryDecodeRootIntoScalar / Map / SliceOfScalar / Struct (each
// expects to consume the buffer exactly).
func TestDecodeTrailingBytesTyped(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		in        any
		newTarget func() any
	}
	tests := []tc{
		{"scalar + junk into *int64", int64(7), func() any { return new(int64) }},
		{"slice-of-scalar + junk into *[]int64", []int64{1, 2}, func() any { return new([]int64) }},
		{"map + junk into *map", map[string]int64{"a": 1}, func() any { return new(map[string]int64) }},
		{"struct + junk into *struct", craftInner{X: 1}, func() any { return new(craftInner) }},
		{"slice-of-struct + junk into *[]struct", []craftInner{{X: 1}}, func() any { return new([]craftInner) }},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		encoded, merr := New().Marshal(tc.in)
		//: the seed must encode cleanly before we append junk.
		if merr != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, merr)
		}
		//: a trailing byte makes the record over-long for the exact typed path.
		withJunk := append(append([]byte{}, encoded...), 0x00)
		//: the trailing-bytes guard must reject the over-long buffer.
		if err := New().Unmarshal(withJunk, tc.newTarget()); err == nil {
			t.Errorf("%s: trailing-bytes Unmarshal succeeded, want error", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDecodeNilSliceOfStructElement covers the nil-element arm of
// decodeSliceElement: a nil record inside a []struct wire payload must decode
// to the element's zero value rather than erroring.
func TestDecodeNilSliceOfStructElement(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"nil element decodes to a zero struct"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: a heterogeneous slice with a nil then a struct, decoded into a
		//: typed []craftInner: element 0 (nil) → zero, element 1 → populated.
		encoded, merr := New().Marshal([]any{nil, craftInner{X: 7, Y: "y"}})
		if merr != nil {
			t.Fatalf("Marshal err=%v", merr)
		}
		var out []craftInner
		//: the nil element must not fail the typed slice-of-struct decode.
		if uerr := New().Unmarshal(encoded, &out); uerr != nil {
			t.Fatalf("Unmarshal err=%v", uerr)
		}
		//: element 0 is the zero struct, element 1 carries the decoded values.
		if len(out) != 2 || out[0] != (craftInner{}) || out[1].X != 7 {
			t.Errorf("decoded = %#v, want [{} {7 y}]", out)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDecodeNilIntoTypedScalar covers the nil-source arm of
// tryDecodeRootIntoScalar: a tagNil record decoded into a typed scalar must
// zero the destination rather than error.
func TestDecodeNilIntoTypedScalar(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"tagNil zeroes a typed int target"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: Marshal(nil) emits a tagNil record.
		encoded, merr := New().Marshal(nil)
		if merr != nil {
			t.Fatalf("Marshal(nil) err=%v", merr)
		}
		//: pre-seed a non-zero target so the zeroing is observable.
		out := int64(99)
		if uerr := New().Unmarshal(encoded, &out); uerr != nil {
			t.Fatalf("Unmarshal err=%v", uerr)
		}
		//: the nil source must have zeroed the scalar.
		if out != 0 {
			t.Errorf("out = %d, want 0 (nil source zeroes the scalar)", out)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDecodeLargeScalarSlice drives the over-budget (length > sliceHintCap)
// growing path of walkScalarSlice: a slice declaring more than the 4096-entry
// pre-alloc bucket must still round-trip via reflect.Append growth.
func TestDecodeLargeScalarSlice(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		size int
	}
	tests := []tc{
		{"five thousand elements exceed the slice hint", sliceHintCap + 904},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: build a slice past the pre-alloc hint so the growing walker runs.
		in := make([]int64, tc.size)
		for i := range in {
			//: distinct values so a mis-indexed growth would be visible.
			in[i] = int64(i)
		}
		encoded, merr := New().Marshal(in)
		//: the large slice must encode cleanly.
		if merr != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, merr)
		}
		var out []int64
		//: decode into the typed slice — drives walkScalarSliceGrowing.
		if uerr := New().Unmarshal(encoded, &out); uerr != nil {
			t.Fatalf("%s: Unmarshal err=%v", tc.name, uerr)
		}
		//: every element must survive the grow-as-we-go walk. Length is
		//: checked first so a short-decode failure doesn't index past
		//: out — out[tc.size-1] would panic on len(out) == 0 and mask
		//: the real failure.
		if len(out) != tc.size {
			t.Errorf("%s: len=%d, want len=%d", tc.name, len(out), tc.size)
			return
		}
		if out[tc.size-1] != int64(tc.size-1) {
			t.Errorf("%s: last=%d, want last=%d", tc.name, out[tc.size-1], tc.size-1)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDecodeFieldNameVarintError drives the field-name varint-failure arm of
// decodeFieldName: a struct field whose name record carries a malformed
// (continuation-only) length varint must be rejected.
func TestDecodeFieldNameVarintError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"continuation-only name length varint"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: struct with one field whose name record is [tagString, 0x80] — a
		//: lone continuation byte that never terminates the varint.
		data := appendTagLen(nil, tagStruct, 1)
		data = append(data, byte(tagString), varintContinuationBit)
		var out any
		//: the malformed name varint must surface a decode error.
		if err := New().Unmarshal(data, &out); err == nil {
			t.Error("malformed field-name varint decoded without error")
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDecodeOversizeFieldName drives the maxFieldNameBytes guard (255): a
// struct record whose field-name string declares more than 255 bytes must be
// rejected on both the generic and typed struct decode paths.
func TestDecodeOversizeFieldName(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		newTarget func() any
	}
	tests := []tc{
		{"generic struct target", func() any { return new(any) }},
		{"typed struct target", func() any { return new(craftInner) }},
	}
	//: one struct field whose name record declares 256 bytes (> the 255 cap).
	oversize := appendTagLen(nil, tagStruct, 1)
	oversize = appendTagLen(oversize, tagString, uint64(maxFieldNameBytes+1))
	//: pad the body so the cap check (not a truncation) is what rejects it.
	oversize = append(oversize, make([]byte, maxFieldNameBytes+1)...)
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: the field-name cap must reject the over-long name.
		err := New().Unmarshal(oversize, tc.newTarget())
		//: any non-nil decode error is acceptable — the cap surfaces as a
		//: decode failure on both the generic and typed struct paths.
		if err == nil {
			t.Errorf("%s: oversize field name decoded without error", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
