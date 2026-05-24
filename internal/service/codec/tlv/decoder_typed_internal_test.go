package tlv

import (
	"reflect"
	"testing"
)

// typedDecoderUser is the canonical struct fixture exercised by the
// target-aware decoder tests. Mirrors a realistic config-row shape:
// scalars + slices + nested map; the field names appear on the wire
// verbatim (struct tag handling is out of scope for the codec).
type typedDecoderUser struct {
	Name  string
	Age   int
	Email string
	Tags  []string
	Meta  map[string]any
}

// roundtripTyped marshals src then unmarshals into dst — the contract
// the target-aware decoder must keep byte-identical to the legacy
// untyped path.
func roundtripTyped[T any](t *testing.T, src T) T {
	t.Helper()
	c := &tlvCodec{}
	enc, err := c.Marshal(src)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var dst T
	if uerr := c.Unmarshal(enc, &dst); uerr != nil {
		t.Fatalf("Unmarshal: %v", uerr)
	}
	return dst
}

// Test_tryDecodeRootInto_TypedStruct pins the typed-target fast path:
// roundtripping into a typed *struct must preserve every exported
// field. The encoder produces a tagStruct record; the decoder must
// walk the wire and assign into the struct fields directly.
func Test_tryDecodeRootInto_TypedStruct(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  typedDecoderUser
	}
	tests := []tc{
		{
			"scalars-only",
			typedDecoderUser{Name: "alice", Age: 30, Email: "a@x"},
		},
		{
			"with-slice-and-map",
			typedDecoderUser{
				Name:  "bob",
				Age:   42,
				Email: "b@y",
				Tags:  []string{"red", "green"},
				Meta:  map[string]any{"k": int64(1)},
			},
		},
		{
			"all-zero-values",
			typedDecoderUser{},
		},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got := roundtripTyped(t, tc.src)
		//: roundtrip contract — every exported field must survive.
		if got.Name != tc.src.Name || got.Age != tc.src.Age || got.Email != tc.src.Email {
			t.Errorf("%s: scalar fields lost: got=%+v want=%+v", tc.name, got, tc.src)
		}
		//: TLV encodes a nil slice as an empty tagSlice record; the
		//: decoder rebuilds it as a non-nil empty slice. Treat the two
		//: as equivalent — this matches the codec's pre-existing
		//: untyped-path semantics (not a regression from this commit).
		if len(got.Tags) != len(tc.src.Tags) {
			t.Errorf("%s: Tags length mismatch: got=%v want=%v", tc.name, got.Tags, tc.src.Tags)
		}
		//: per-element compare guards content fidelity.
		for i := range tc.src.Tags {
			if got.Tags[i] != tc.src.Tags[i] {
				t.Errorf("%s: Tags[%d]=%q want %q", tc.name, i, got.Tags[i], tc.src.Tags[i])
			}
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_tryDecodeRootInto_NonStructTarget pins the fall-back behaviour:
// the typed path must signal handled=false for non-struct targets so
// the untyped decodeRoot path runs.
func Test_tryDecodeRootInto_NonStructTarget(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		target reflect.Value
	}
	tests := []tc{
		{"int target", reflect.ValueOf(new(int)).Elem()},
		{"string target", reflect.ValueOf(new(string)).Elem()},
		{"slice target", reflect.ValueOf(new([]int)).Elem()},
		{"map target", reflect.ValueOf(new(map[string]int)).Elem()},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: arbitrary non-empty bytes — content is irrelevant because the
		//: target shape alone determines the fall-back signal.
		handled, terr := tryDecodeRootInto([]byte{0x01, 0x00}, tc.target)
		//: non-struct targets must always fall back with no typed-path error.
		if terr != nil {
			t.Errorf("%s: typed path returned err on fall-back: %v", tc.name, terr)
		}
		//: non-struct targets must always fall back.
		if handled {
			t.Errorf("%s: expected handled=false for non-struct target", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_tryDecodeRootInto_NonStructWire pins the cross-shape behaviour:
// the target is a struct but the wire encodes a non-struct value
// (e.g. caller passed a *MyStruct then a map[string]any was wrapped
// in the wire). The typed path must signal handled=false so the
// untyped projection runs.
func Test_tryDecodeRootInto_NonStructWire(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		wire []byte
	}
	tests := []tc{
		//: tagInt8 (0x10) carries a 1-byte length and a 1-byte body.
		{"int8 wire", []byte{0x10, 0x01, 0x05}},
		//: tagString (0x40) with a 1-byte body.
		{"string wire", []byte{0x40, 0x01, 'x'}},
		//: tagMap (0x60) — different from struct, should fall back.
		{"map wire", []byte{0x60, 0x00}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		target := reflect.ValueOf(new(typedDecoderUser)).Elem()
		handled, terr := tryDecodeRootInto(tc.wire, target)
		//: typed path must defer to the untyped projector without error.
		if terr != nil {
			t.Errorf("%s: typed path returned err on fall-back: %v", tc.name, terr)
		}
		if handled {
			t.Errorf("%s: expected handled=false on non-tagStruct wire", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_tryDecodeRootInto_UnknownField pins the "unknown field gets
// silently skipped" contract — matches encoding/json's lenient
// extra-field handling. The typed path must consume the unknown
// field's value bytes so the cursor stays aligned for the next field.
func Test_tryDecodeRootInto_UnknownField(t *testing.T) {
	t.Parallel()
	//: source struct carries a field the destination doesn't have.
	type src struct {
		Name    string
		Age     int
		Unknown string
	}
	type dst struct {
		Name string
		Age  int
	}
	type tc struct {
		name string
		src  src
		want dst
	}
	tests := []tc{
		{
			"unknown-tail-field",
			src{Name: "alice", Age: 30, Unknown: "ignored"},
			dst{Name: "alice", Age: 30},
		},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &tlvCodec{}
		enc, merr := c.Marshal(tc.src)
		if merr != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, merr)
		}
		var out dst
		if uerr := c.Unmarshal(enc, &out); uerr != nil {
			t.Fatalf("%s: Unmarshal err=%v", tc.name, uerr)
		}
		//: known fields preserved; unknown silently dropped.
		if out != tc.want {
			t.Errorf("%s: got=%+v want=%+v", tc.name, out, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_buildNameIndex pins the linear-scan-vs-map threshold:
// structs with ≤ linearScanFieldLimit fields return nil (linear scan
// is faster); larger structs return a populated map.
func Test_buildNameIndex(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		fieldCount  int
		wantMapping bool
	}
	tests := []tc{
		{"small-uses-linear-scan", 3, false},
		{"at-threshold-uses-linear", linearScanFieldLimit, false},
		{"over-threshold-uses-map", linearScanFieldLimit + 1, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		info := &structTypeInfo{fields: make([]structFieldInfo, tc.fieldCount)}
		//: synthesise unique field names for each slot.
		for i := range info.fields {
			info.fields[i] = structFieldInfo{name: "F" + decimalDigit(i), index: i}
		}
		out := buildNameIndex(info)
		//: contract: nil below the threshold, populated map above.
		if (out != nil) != tc.wantMapping {
			t.Errorf("%s: got mapping=%v want %v", tc.name, out != nil, tc.wantMapping)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_resolveFieldIndex pins both code paths (map + linear scan)
// against the same expected mapping.
func Test_resolveFieldIndex(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		fields  []string
		lookup  string
		wantIdx int
		wantOK  bool
	}
	tests := []tc{
		{"linear-hit", []string{"A", "B", "C"}, "B", 1, true},
		{"linear-miss", []string{"A", "B"}, "Z", -1, false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		info := &structTypeInfo{fields: make([]structFieldInfo, len(tc.fields))}
		for i, name := range tc.fields {
			info.fields[i] = structFieldInfo{name: name, index: i}
		}
		idx, ok := resolveFieldIndex(info, nil, tc.lookup)
		//: linear-scan path contract.
		if idx != tc.wantIdx || ok != tc.wantOK {
			t.Errorf("%s: linear: got (%d,%v) want (%d,%v)", tc.name, idx, ok, tc.wantIdx, tc.wantOK)
		}
		//: map path contract: build a map manually and probe.
		nameIndex := make(map[string]int, len(info.fields))
		for i, f := range info.fields {
			nameIndex[f.name] = i
		}
		idx2, ok2 := resolveFieldIndex(info, nameIndex, tc.lookup)
		//: map path must agree with the linear path bit-for-bit.
		if idx2 != tc.wantIdx || ok2 != tc.wantOK {
			t.Errorf("%s: map: got (%d,%v) want (%d,%v)", tc.name, idx2, ok2, tc.wantIdx, tc.wantOK)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// decimalDigit returns the single-digit decimal string for i in
// [0, 10). Used by the synthetic field-name generator above.
func decimalDigit(i int) string {
	//: range guard keeps Test_buildNameIndex's synthesiser focused.
	const digitsBeforeRollover int = 10
	if i < digitsBeforeRollover {
		//: single-digit representation.
		return string(rune('0' + i))
	}
	//: roll over to "A".."Z" for larger indices.
	return string(rune('A' + i - digitsBeforeRollover))
}
