package tlv

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"testing"
)

// Test_appendTagLen covers the tag+length writer used by every encode path.
func Test_appendTagLen(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		tag    Tag
		length uint64
	}
	tests := []tc{{"nil tag, zero length", tagNil, 0}, {"large length", tagString, 200}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out := appendTagLen(nil, tc.tag, tc.length)
		if len(out) == 0 || out[0] != byte(tc.tag) {
			t.Errorf("%s: missing tag byte", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_encodeValue covers the top-level reflection dispatch.
func Test_encodeValue(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		value   any
		wantErr bool
	}
	tests := []tc{
		{"int round-trip", int64(1), false},
		{"depth zero is fine", "x", false},
		{"chan rejected", make(chan int), true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := encodeValue(nil, tc.value, 0)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_encodeByKind covers the kind-based dispatch.
func Test_encodeByKind(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		value   any
		wantErr bool
	}
	tests := []tc{
		{"bool", true, false},
		{"int64", int64(2), false},
		{"uint64", uint64(3), false},
		{"float64", float64(1.5), false},
		{"float32 source", float32(1.5), false},
		{"string", "abc", false},
		{"chan rejected", make(chan int), true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := encodeByKind(nil, reflectView(reflect.ValueOf(tc.value)), 0)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_tryEncodeScalar covers every scalar Kind branch.
func Test_tryEncodeScalar(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		value       any
		wantHandled bool
	}
	tests := []tc{
		{"bool handled", true, true},
		{"int handled", int64(1), true},
		{"uint handled", uint64(1), true},
		{"float32 handled", float32(1), true},
		{"float64 handled", float64(1), true},
		{"string handled", "x", true},
		{"slice not scalar", []int{1}, false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, handled := tryEncodeScalar(nil, reflectView(reflect.ValueOf(tc.value)))
		if handled != tc.wantHandled {
			t.Errorf("%s: handled=%v want %v", tc.name, handled, tc.wantHandled)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_tryEncodeComposite covers every composite Kind branch.
func Test_tryEncodeComposite(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		value       any
		wantHandled bool
	}
	tests := []tc{
		{"slice handled", []int{1}, true},
		{"map handled", map[string]int{"a": 1}, true},
		{"struct handled", struct{ X int }{1}, true},
		{"bool not composite", true, false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, handled, err := tryEncodeComposite(nil, reflectView(reflect.ValueOf(tc.value)), 0)
		if err != nil {
			t.Fatalf("%s: unexpected encode err=%v", tc.name, err)
		}
		if handled != tc.wantHandled {
			t.Errorf("%s: handled=%v want %v", tc.name, handled, tc.wantHandled)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_encodeCompositeSliceLike covers the bytes fast path and the
// generic slice path.
func Test_encodeCompositeSliceLike(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		value any
	}
	tests := []tc{{"bytes", []byte{1, 2}}, {"generic slice", []int{1, 2}}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := encodeCompositeSliceLike(nil, reflectView(reflect.ValueOf(tc.value)), 0)
		if err != nil {
			t.Errorf("%s: err=%v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_encodeBool covers both true and false branches.
func Test_encodeBool(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   bool
		want byte
	}
	tests := []tc{{"false branch", false, byte(tagBoolFalse)}, {"true branch", true, byte(tagBoolTrue)}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out := encodeBool(nil, tc.in)
		if len(out) == 0 || out[0] != tc.want {
			t.Errorf("%s: tag=%d want %d", tc.name, out[0], tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_encodeInt covers every signed-width narrowing branch.
func Test_encodeInt(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   int64
		tag  byte
	}
	tests := []tc{
		{"int8 range", 1, byte(tagInt8)},
		{"int16 range", 1000, byte(tagInt16)},
		{"int32 range", 1 << 20, byte(tagInt32)},
		{"int64 range", 1 << 40, byte(tagInt64)},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out := encodeInt(nil, tc.in)
		if out[0] != tc.tag {
			t.Errorf("%s: tag=%d want %d", tc.name, out[0], tc.tag)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_encodeUint covers every unsigned-width narrowing branch.
func Test_encodeUint(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   uint64
		tag  byte
	}
	tests := []tc{
		{"uint8 range", 1, byte(tagUint8)},
		{"uint16 range", 1000, byte(tagUint16)},
		{"uint32 range", 1 << 20, byte(tagUint32)},
		{"uint64 range", 1 << 40, byte(tagUint64)},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out := encodeUint(nil, tc.in)
		if out[0] != tc.tag {
			t.Errorf("%s: tag=%d want %d", tc.name, out[0], tc.tag)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_encodeFloat32 covers the 4-byte float branch.
func Test_encodeFloat32(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   float32
	}
	tests := []tc{{"single precision", 1.5}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out := encodeFloat32(nil, tc.in)
		if out[0] != byte(tagFloat32) {
			t.Errorf("%s: wrong tag %d", tc.name, out[0])
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_encodeFloat64 covers the 8-byte float branch.
func Test_encodeFloat64(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   float64
	}
	tests := []tc{{"double precision", 1.5}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out := encodeFloat64(nil, tc.in)
		if out[0] != byte(tagFloat64) {
			t.Errorf("%s: wrong tag %d", tc.name, out[0])
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_encodeString covers the UTF-8 string branch.
func Test_encodeString(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
	}
	tests := []tc{{"non-empty", "abc"}, {"empty", ""}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out := encodeString(nil, tc.in)
		if out[0] != byte(tagString) {
			t.Errorf("%s: wrong tag %d", tc.name, out[0])
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_encodeBytes covers the opaque bytes branch.
func Test_encodeBytes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   []byte
	}
	tests := []tc{{"non-empty", []byte{1, 2, 3}}, {"empty", []byte{}}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out := encodeBytes(nil, tc.in)
		if out[0] != byte(tagBytes) {
			t.Errorf("%s: wrong tag %d", tc.name, out[0])
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_bytesFromReflectValue covers both slice and array branches.
func Test_bytesFromReflectValue(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   any
	}
	tests := []tc{{"slice", []byte{1, 2}}, {"array", [2]byte{3, 4}}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out := bytesFromReflectValue(reflectView(reflect.ValueOf(tc.in)))
		if len(out) != 2 {
			t.Errorf("%s: len=%d want 2", tc.name, len(out))
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_encodeSliceDirect covers the direct-into-buffer slice encoder
// (replaces the legacy collect-then-emit pair).
func Test_encodeSliceDirect(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   any
	}
	tests := []tc{
		{"int slice", []int{1, 2, 3}},
		{"string slice", []string{"a", "b"}},
		{"empty slice", []int{}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out, err := encodeSliceDirect(nil, reflectView(reflect.ValueOf(tc.in)), 0)
		if err != nil {
			t.Errorf("%s: err=%v", tc.name, err)
		}
		//: contract: header byte must be the slice tag.
		if len(out) == 0 || out[0] != byte(tagSlice) {
			t.Errorf("%s: missing tagSlice header in %v", tc.name, out)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_encodeMapDirect covers the direct-into-buffer map encoder
// (replaces the legacy collect-then-emit pair).
func Test_encodeMapDirect(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   any
	}
	tests := []tc{
		{"single pair", map[string]int{"a": 1}},
		{"two pairs", map[string]int{"a": 1, "b": 2}},
		{"empty map", map[string]int{}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out, err := encodeMapDirect(nil, reflectView(reflect.ValueOf(tc.in)), 0)
		if err != nil {
			t.Errorf("%s: err=%v", tc.name, err)
		}
		//: contract: header byte must be the map tag.
		if len(out) == 0 || out[0] != byte(tagMap) {
			t.Errorf("%s: missing tagMap header in %v", tc.name, out)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_encodeStructDirect covers the direct-into-buffer struct
// encoder (replaces the legacy collect-then-emit pair).
func Test_encodeStructDirect(t *testing.T) {
	t.Parallel()
	type sample struct {
		Public  int
		private int //nolint:unused // intentionally unexported for the test
	}
	type tc struct {
		name    string
		in      any
		wantErr bool
	}
	tests := []tc{
		{"single exported field encoded", sample{Public: 1, private: 2}, false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out, err := encodeStructDirect(nil, reflectView(reflect.ValueOf(tc.in)), 0)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
		//: contract: header byte must be the struct tag.
		if !tc.wantErr && (len(out) == 0 || out[0] != byte(tagStruct)) {
			t.Errorf("%s: missing tagStruct header in %v", tc.name, out)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_encodeStructDirect_OversizeFieldName pins the field-name cap
// gate — an exported field whose name exceeds maxFieldNameBytes must
// surface as a marshal failure, not silently truncate.
func Test_encodeStructDirect_OversizeFieldName(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		nameLen   int
		wantError bool
	}
	tests := []tc{
		{"at-limit-accepts", maxFieldNameBytes, false},
		{"over-limit-rejects", maxFieldNameBytes + 1, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: synthesise a struct field whose name is exactly tc.nameLen
		//: long via reflect.StructOf. Field names must be valid Go
		//: identifiers so we fill with repeated 'A' bytes.
		buf := make([]byte, tc.nameLen)
		for i := range buf {
			//: 'A' is a stable valid identifier character at any position.
			buf[i] = 'A'
		}
		typ := reflect.StructOf([]reflect.StructField{
			{Name: string(buf), Type: reflect.TypeFor[int]()},
		})
		val := reflect.New(typ).Elem()
		_, err := encodeStructDirect(nil, reflectView(val), 0)
		//: contract: only over-limit names trigger the rejection.
		if (err != nil) != tc.wantError {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantError)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_unsupportedKind covers the sentinel wrapper.
func Test_unsupportedKind(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"surfaces a non-nil error"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if err := unsupportedKind("chan"); err == nil {
			t.Errorf("%s: nil error", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_depthExceededError covers the sentinel wrapper.
func Test_depthExceededError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"surfaces a non-nil error"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if err := depthExceededError(99); err == nil {
			t.Errorf("%s: nil error", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_fieldNameTooLong covers the sentinel wrapper.
func Test_fieldNameTooLong(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"surfaces a non-nil error"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if err := fieldNameTooLong(maxFieldNameBytes + 1); err == nil {
			t.Errorf("%s: nil error", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_readOneRecord covers reading one TLV from an io.Reader.
func Test_readOneRecord(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		wantEOF bool
		wantErr bool
	}
	tests := []tc{
		{"empty reader returns EOF", nil, true, false},
		{"truncated mid-length", []byte{0x40, 0x80}, false, true},
		{"valid nil record", []byte{0x01, 0x00}, false, false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := readOneRecord(bytes.NewReader(tc.data))
		if tc.wantEOF && !errors.Is(err, io.EOF) {
			t.Errorf("%s: expected EOF, got %v", tc.name, err)
		}
		if tc.wantErr && err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
		if !tc.wantEOF && !tc.wantErr && err != nil {
			t.Errorf("%s: unexpected err %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_readTag covers single-byte tag reads.
func Test_readTag(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		wantEOF bool
	}
	tests := []tc{{"empty returns EOF", nil, true}, {"single byte returns tag", []byte{0x40}, false}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := readTag(bytes.NewReader(tc.data))
		if tc.wantEOF && !errors.Is(err, io.EOF) {
			t.Errorf("%s: expected EOF, got %v", tc.name, err)
		}
		if !tc.wantEOF && err != nil {
			t.Errorf("%s: unexpected err %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_assembleRecord covers the record-rebuild helper.
func Test_assembleRecord(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		length  uint64
		body    []byte
		wantErr bool
	}
	tests := []tc{
		{"length matches body", 3, []byte{1, 2, 3}, false},
		{"short body truncates", 4, []byte{1, 2}, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := assembleRecord(bytes.NewReader(tc.body), 0x40, tc.length)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_readUvarint covers varint parsing from a stream.
func Test_readUvarint(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		want    uint64
		wantErr bool
	}
	tests := []tc{
		{"single-byte 7", []byte{0x07}, 7, false},
		{"truncated continuation", []byte{0x80}, 0, true},
		{"overflow", []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x02}, 0, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		val, err := readUvarint(bytes.NewReader(tc.data))
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
		if !tc.wantErr && val != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, val, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_wrapReadFailure covers the I/O error wrapper.
func Test_wrapReadFailure(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   error
	}
	tests := []tc{{"unexpected EOF", io.ErrUnexpectedEOF}, {"generic error", errors.New("other")}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if err := wrapReadFailure(tc.in); err == nil {
			t.Errorf("%s: nil err", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_decodeRoot covers Unmarshal's pointer-shape guards.
func Test_decodeRoot(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		target  any
		wantErr bool
	}
	tests := []tc{
		{"non-pointer target rejected", []byte{0x01, 0x00}, "x", true},
		{"valid nil record", []byte{0x01, 0x00}, new(any), false},
		{"trailing bytes rejected", []byte{0x01, 0x00, 0xff}, new(any), true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := decodeRoot(tc.data, tc.target)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_decodeValue covers the top-level decode dispatch.
func Test_decodeValue(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		wantErr bool
	}
	tests := []tc{
		{"short buffer truncates", []byte{0x40}, true},
		{"valid nil record", []byte{0x01, 0x00}, false},
		{"depth ok", []byte{0x20, 0x01, 0x07}, false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, _, err := decodeValue(tc.data, 0)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_decodeByTag covers the band-based dispatch.
func Test_decodeByTag(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		tag     Tag
		length  uint64
		rest    []byte
		wantErr bool
	}
	tests := []tc{
		{"slice with zero elements", tagSlice, 0, nil, false},
		{"map with zero pairs", tagMap, 0, nil, false},
		{"struct with zero fields", tagStruct, 0, nil, false},
		{"unknown tag", Tag(0xff), 0, nil, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, _, err := decodeByTag(tc.tag, tc.length, tc.rest, 0)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_decodeScalarByTag covers the scalar band dispatch.
func Test_decodeScalarByTag(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		tag     Tag
		length  uint64
		rest    []byte
		wantErr bool
	}
	tests := []tc{
		{"nil zero length", tagNil, 0, nil, false},
		{"int8 with one byte", tagInt8, 1, []byte{0x01}, false},
		{"string zero length", tagString, 0, nil, false},
		{"unknown tag", Tag(0xff), 0, nil, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, _, err := decodeScalarByTag(tc.tag, tc.length, tc.rest)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_tryZeroPayloadTag covers the zero-payload dispatch table.
func Test_tryZeroPayloadTag(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		tag         Tag
		wantHandled bool
	}
	tests := []tc{
		{"nil handled", tagNil, true},
		{"false handled", tagBoolFalse, true},
		{"true handled", tagBoolTrue, true},
		{"int not handled", tagInt8, false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, _, handled := tryZeroPayloadTag(tc.tag, nil)
		if handled != tc.wantHandled {
			t.Errorf("%s: handled=%v want %v", tc.name, handled, tc.wantHandled)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_tryNumericTag covers the numeric dispatch table.
func Test_tryNumericTag(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		tag         Tag
		length      uint64
		rest        []byte
		wantHandled bool
	}
	tests := []tc{
		{"int8 handled", tagInt8, 1, []byte{0x01}, true},
		{"uint8 handled", tagUint8, 1, []byte{0x01}, true},
		{"float32 handled", tagFloat32, 4, []byte{0, 0, 0, 0}, true},
		{"string not numeric", tagString, 0, nil, false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, _, handled, err := tryNumericTag(tc.tag, tc.length, tc.rest)
		if err != nil {
			t.Fatalf("%s: unexpected numeric tag err=%v", tc.name, err)
		}
		if handled != tc.wantHandled {
			t.Errorf("%s: handled=%v want %v", tc.name, handled, tc.wantHandled)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_readVarintFromBytes covers in-buffer varint reads.
func Test_readVarintFromBytes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      []byte
		want    uint64
		wantErr bool
	}
	tests := []tc{
		{"single byte", []byte{0x07}, 7, false},
		{"empty buffer", nil, 0, true},
		{"overflow", []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x02}, 0, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		val, _, err := readVarintFromBytes(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
		if !tc.wantErr && val != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, val, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_decodeInt covers every signed-integer width.
func Test_decodeInt(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		tag  Tag
		rest []byte
	}
	tests := []tc{
		{"int8", tagInt8, []byte{0x01}},
		{"int16", tagInt16, []byte{0x00, 0x01}},
		{"int32", tagInt32, []byte{0, 0, 0, 1}},
		{"int64", tagInt64, []byte{0, 0, 0, 0, 0, 0, 0, 1}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, _, err := decodeInt(tc.tag, uint64(len(tc.rest)), tc.rest)
		if err != nil {
			t.Errorf("%s: err=%v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_decodeUint covers every unsigned-integer width.
func Test_decodeUint(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		tag  Tag
		rest []byte
	}
	tests := []tc{
		{"uint8", tagUint8, []byte{0x01}},
		{"uint16", tagUint16, []byte{0x00, 0x01}},
		{"uint32", tagUint32, []byte{0, 0, 0, 1}},
		{"uint64", tagUint64, []byte{0, 0, 0, 0, 0, 0, 0, 1}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, _, err := decodeUint(tc.tag, uint64(len(tc.rest)), tc.rest)
		if err != nil {
			t.Errorf("%s: err=%v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_decodeFloat covers both float widths.
func Test_decodeFloat(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		tag  Tag
		rest []byte
	}
	tests := []tc{
		{"float32", tagFloat32, []byte{0, 0, 0, 0}},
		{"float64", tagFloat64, []byte{0, 0, 0, 0, 0, 0, 0, 0}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, _, err := decodeFloat(tc.tag, uint64(len(tc.rest)), tc.rest)
		if err != nil {
			t.Errorf("%s: err=%v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_decodeString covers the UTF-8 string payload.
func Test_decodeString(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		length  uint64
		rest    []byte
		wantErr bool
	}
	tests := []tc{{"non-empty", 3, []byte("abc"), false}, {"short buffer", 5, []byte("ab"), true}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, _, err := decodeString(tc.length, tc.rest)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_decodeBytes covers the opaque bytes payload.
func Test_decodeBytes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		length  uint64
		rest    []byte
		wantErr bool
	}
	tests := []tc{{"non-empty", 2, []byte{0x10, 0x20}, false}, {"short buffer", 5, []byte{1}, true}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, _, err := decodeBytes(tc.length, tc.rest)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_decodeSlice covers the slice container decoder.
func Test_decodeSlice(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		length  uint64
		rest    []byte
		wantErr bool
	}
	tests := []tc{
		{"zero elements", 0, nil, false},
		{"one int8 element", 1, []byte{0x10, 0x01, 0x07}, false},
		{"truncated element", 1, []byte{0x10, 0x01}, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, _, err := decodeSlice(tc.length, tc.rest, 0)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_decodeMap covers the map container decoder.
func Test_decodeMap(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		length  uint64
		rest    []byte
		wantErr bool
	}
	//: key = uint8(1); value = uint8(2)
	pair := []byte{0x20, 0x01, 0x01, 0x20, 0x01, 0x02}
	tests := []tc{
		{"zero pairs", 0, nil, false},
		{"one pair", 1, pair, false},
		{"truncated value", 1, []byte{0x20, 0x01, 0x01}, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, _, err := decodeMap(tc.length, tc.rest, 0)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_decodeStruct covers the struct container decoder.
func Test_decodeStruct(t *testing.T) {
	t.Parallel()
	//: field name = "x" (string, len=1); value = uint8(7)
	field := []byte{0x40, 0x01, 'x', 0x20, 0x01, 0x07}
	type tc struct {
		name    string
		length  uint64
		rest    []byte
		wantErr bool
	}
	tests := []tc{
		{"zero fields", 0, nil, false},
		{"one field", 1, field, false},
		{"non-string name", 1, []byte{0x20, 0x01, 0x01, 0x20, 0x01, 0x07}, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, _, err := decodeStruct(tc.length, tc.rest, 0)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_assignDecoded covers the publish-through-pointer helper.
func Test_assignDecoded(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		value   any
		wantErr bool
	}
	tests := []tc{{"int into int target", int64(42), false}, {"nil into any target", nil, false}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var dst any
		ptr := reflect.ValueOf(&dst).Elem()
		err := assignDecoded(reflectView(ptr), ptr.Type(), tc.value)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_convertValue covers reflect-based conversion narrowing.
func Test_convertValue(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		value   any
		target  reflect.Type
		wantErr bool
	}
	tests := []tc{
		{"int into int target", int64(7), reflect.TypeFor[int](), false},
		{"string into int target", "abc", reflect.TypeFor[int](), true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := convertValue(tc.value, tc.target)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_narrowNumeric covers the numeric dispatch.
func Test_narrowNumeric(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		value       any
		target      reflect.Type
		wantHandled bool
	}
	tests := []tc{
		{"int handled", int64(1), reflect.TypeFor[int](), true},
		{"uint handled", uint64(1), reflect.TypeFor[uint](), true},
		{"float handled", float64(1), reflect.TypeFor[float32](), true},
		{"string skipped", "x", reflect.TypeFor[string](), false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, ok := narrowNumeric(tc.value, tc.target)
		if ok != tc.wantHandled {
			t.Errorf("%s: ok=%v want %v", tc.name, ok, tc.wantHandled)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_narrowFromInt covers signed-int narrowing.
func Test_narrowFromInt(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		target      reflect.Type
		wantHandled bool
	}
	tests := []tc{
		{"int target", reflect.TypeFor[int](), true},
		{"uint target", reflect.TypeFor[uint](), true},
		{"string target", reflect.TypeFor[string](), false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, ok := narrowFromInt(1, tc.target)
		if ok != tc.wantHandled {
			t.Errorf("%s: ok=%v want %v", tc.name, ok, tc.wantHandled)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_narrowFromUint covers unsigned-int narrowing.
func Test_narrowFromUint(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		target      reflect.Type
		wantHandled bool
	}
	tests := []tc{
		{"int target", reflect.TypeFor[int](), true},
		{"uint target", reflect.TypeFor[uint](), true},
		{"string target", reflect.TypeFor[string](), false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, ok := narrowFromUint(1, tc.target)
		if ok != tc.wantHandled {
			t.Errorf("%s: ok=%v want %v", tc.name, ok, tc.wantHandled)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_narrowFromFloat covers float narrowing.
func Test_narrowFromFloat(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		target      reflect.Type
		wantHandled bool
	}
	tests := []tc{
		{"float32 target", reflect.TypeFor[float32](), true},
		{"float64 target", reflect.TypeFor[float64](), true},
		{"int target", reflect.TypeFor[int](), false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, ok := narrowFromFloat(1.0, tc.target)
		if ok != tc.wantHandled {
			t.Errorf("%s: ok=%v want %v", tc.name, ok, tc.wantHandled)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_truncatedError surfaces the TRUNCATED sentinel.
func Test_truncatedError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"surfaces non-nil"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if truncatedError() == nil {
			t.Errorf("%s: nil err", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_malformedLengthError surfaces the malformed-length sentinel.
func Test_malformedLengthError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"surfaces non-nil"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if malformedLengthError(1, 2) == nil {
			t.Errorf("%s: nil err", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_unknownTagError surfaces the unknown-tag sentinel.
func Test_unknownTagError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"surfaces non-nil"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if unknownTagError(Tag(0xff)) == nil {
			t.Errorf("%s: nil err", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_sizeExceededError surfaces the SIZE_EXCEEDED sentinel.
func Test_sizeExceededError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"surfaces non-nil"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if sizeExceededError(1<<30) == nil {
			t.Errorf("%s: nil err", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_cannotSetError surfaces the unsettable-target sentinel.
func Test_cannotSetError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"surfaces non-nil"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if cannotSetError() == nil {
			t.Errorf("%s: nil err", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_unhashableKeyError surfaces the unhashable-key sentinel.
func Test_unhashableKeyError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"surfaces non-nil"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if unhashableKeyError() == nil {
			t.Errorf("%s: nil err", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_nonStringFieldNameError surfaces the non-string-field-name sentinel.
func Test_nonStringFieldNameError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"surfaces non-nil"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if nonStringFieldNameError() == nil {
			t.Errorf("%s: nil err", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_nonPointerTargetError surfaces the non-pointer-target sentinel.
func Test_nonPointerTargetError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"surfaces non-nil"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if nonPointerTargetError() == nil {
			t.Errorf("%s: nil err", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_trailingBytesError surfaces the trailing-bytes sentinel.
func Test_trailingBytesError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"surfaces non-nil"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if trailingBytesError(3) == nil {
			t.Errorf("%s: nil err", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_varintOverflowError surfaces the varint-overflow sentinel.
func Test_varintOverflowError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"surfaces non-nil"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if varintOverflowError() == nil {
			t.Errorf("%s: nil err", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_incompatibleTypeError surfaces the incompatible-type sentinel.
func Test_incompatibleTypeError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"surfaces non-nil"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if incompatibleTypeError("from", "to") == nil {
			t.Errorf("%s: nil err", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
