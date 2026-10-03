// Package bson — the decoder: which BSON types each Go target accepts, what
// an interface receives, how structs, slices and maps are filled. The rules
// are the previous library's, checked against it in a differential run before
// it left the module graph.
package bson

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// decodeHolder holds one target field under the element name "v".
type decodeHolder[T any] struct {
	// V is the target.
	V T `bson:"v"`
}

// oneElement encodes {"v": value} with the codec under test.
func oneElement(t *testing.T, value any) []byte {
	t.Helper()
	data, err := New().Marshal(D{{Key: "v", Value: value}})
	//: a fixture the encoder refuses would test nothing.
	if err != nil {
		t.Fatalf("building {v: %T}: %v", value, err)
	}
	return data
}

// conversion is one BSON value decoded into one Go target.
type conversion struct {
	name    string
	bson    any
	decode  func(t *testing.T, data []byte) (any, error)
	want    any
	wantErr bool
}

// into decodes {"v": …} into a decodeHolder[T] and returns V.
func into[T any]() func(t *testing.T, data []byte) (any, error) {
	//: a fresh holder per call.
	return func(t *testing.T, data []byte) (any, error) {
		t.Helper()
		var h decodeHolder[T]
		err := New().Unmarshal(data, &h)
		return h.V, err
	}
}

// conversionCases is the accepted-and-refused table.
func conversionCases() []conversion {
	when := time.Date(2026, time.January, 2, 3, 4, 5, 678_000_000, time.UTC)
	oid := ObjectID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	return []conversion{
		{"int32 into int8", int32(-5), into[int8](), int8(-5), false},
		{"int32 into uint16", int32(65535), into[uint16](), uint16(65535), false},
		{"int32 into float32", int32(3), into[float32](), float32(3), false},
		{"int32 into bool", int32(2), into[bool](), true, false},
		{"int32 into string", int32(1), into[string](), nil, true},
		{"int32 past int8", int32(300), into[int8](), nil, true},
		{"negative into uint", int32(-1), into[uint](), nil, true},
		{"int64 into int", int64(1 << 30), into[int](), 1 << 30, false},
		{"int64 past int32", int64(1 << 40), into[int32](), nil, true},
		{"int64 into float64", int64(1 << 40), into[float64](), float64(1 << 40), false},
		{"whole double into int", 2.0, into[int](), 2, false},
		{"fractional double into int", 2.5, into[int](), nil, true},
		{"infinite double into int64", math.Inf(1), into[int64](), nil, true},
		{"double into float32 exactly", 1.5, into[float32](), float32(1.5), false},
		{"double float32 would round", 0.1, into[float32](), nil, true},
		{"double into bool", 0.5, into[bool](), true, false},
		{"true into int", true, into[int](), 1, false},
		{"true into float64", true, into[float64](), 1.0, false},
		{"null into int", nil, into[int](), 0, false},
		{"undefined into int", Undefined{}, into[int](), 0, false},
		{"null into string", nil, into[string](), "", false},
		{"null into a pointer", nil, into[*int](), (*int)(nil), false},
		{"null into a slice", nil, into[[]int](), []int(nil), false},
		{"null into a map", nil, into[map[string]int](), map[string]int(nil), false},
		{"null into a time", nil, into[time.Time](), time.Time{}, false},
		{"null into an interface", nil, into[any](), nil, false},
		{"string into string", "s", into[string](), "s", false},
		{"string into bytes", "s", into[[]byte](), []byte("s"), false},
		{"string into a Symbol", "s", into[Symbol](), Symbol("s"), false},
		{"string into int", "s", into[int](), nil, true},
		{"symbol into string", Symbol("s"), into[string](), "s", false},
		{"JavaScript into string", JavaScript("x"), into[string](), nil, true},
		{"ObjectID into string", oid, into[string](), "0102030405060708090a0b0c", false},
		{"ObjectID into ObjectID", oid, into[ObjectID](), oid, false},
		{"hex string into ObjectID", "0102030405060708090a0b0c", into[ObjectID](), oid, false},
		{"twelve-byte string into ObjectID", "abcdefghijkl", into[ObjectID](), ObjectID([]byte("abcdefghijkl")), false},
		{"short string into ObjectID", "short", into[ObjectID](), nil, true},
		{"generic binary into bytes", []byte{1, 2}, into[[]byte](), []byte{1, 2}, false},
		{"generic binary into string", []byte("hi"), into[string](), "hi", false},
		{"generic binary into a byte array", []byte{1, 2}, into[[3]byte](), [3]byte{1, 2}, false},
		{"generic binary too long for the array", []byte{1, 2, 3}, into[[2]byte](), nil, true},
		{"UUID binary into bytes", Binary{Subtype: BinaryUUID, Data: []byte{1}}, into[[]byte](), nil, true},
		{"old binary into bytes", Binary{Subtype: BinaryOld, Data: []byte{7}}, into[[]byte](), []byte{7}, false},
		{"binary into Binary", Binary{Subtype: BinaryMD5, Data: []byte{1}}, into[Binary](), Binary{Subtype: BinaryMD5, Data: []byte{1}}, false},
		{"datetime into time", when, into[time.Time](), when, false},
		{"datetime into DateTime", DateTime(55), into[DateTime](), DateTime(55), false},
		{"int64 into time as milliseconds", int64(1500), into[time.Time](), time.UnixMilli(1500).UTC(), false},
		{"timestamp into time as seconds", Timestamp{T: 100, I: 1}, into[time.Time](), time.Unix(100, 0).UTC(), false},
		{"string into time", "2026-01-02T03:04:05.678Z", into[time.Time](), when, false},
		{"malformed string into time", "yesterday", into[time.Time](), nil, true},
		{"int32 into json.Number", int32(5), into[json.Number](), json.Number("5"), false},
		{"double into json.Number", 1.25, into[json.Number](), json.Number("1.25"), false},
		{"string into url.URL", "https://a.b/c", into[url.URL](), url.URL{Scheme: "https", Host: "a.b", Path: "/c"}, false},
		{"array into a slice", A{int32(1), int64(2), 3.0}, into[[]int](), []int{1, 2, 3}, false},
		{"empty array into a slice", A{}, into[[]int](), []int{}, false},
		{"array into a short array", A{int32(1)}, into[[2]int](), [2]int{1, 0}, false},
		{"array into a long array", A{int32(1), int32(2), int32(3)}, into[[2]int](), nil, true},
		{"document into a slice", D{{"0", 1}}, into[[]int](), nil, true},
		{"array into named bytes", A{int32(1), int32(2)}, into[namedBytes](), namedBytes{1, 2}, false},
		{"array into []byte", A{int32(1)}, into[[]byte](), nil, true},
		{"document into a map", D{{"a", int32(1)}}, into[map[string]int](), map[string]int{"a": 1}, false},
		{"document into an int-keyed map", D{{"-12", "x"}}, into[map[int]string](), map[int]string{-12: "x"}, false},
		{"bad key for an int-keyed map", D{{"x", "v"}}, into[map[int]string](), nil, true},
		{"document into a D", D{{"a", int32(1)}}, into[D](), D{{"a", int32(1)}}, false},
		{"array into a D", A{int32(1)}, into[D](), nil, true},
		{"undefined into a D", Undefined{}, into[D](), nil, true},
		{"document into a struct", D{{"x", int32(5)}}, into[decodeInner](), decodeInner{X: 5}, false},
		{"string into a struct", "s", into[decodeInner](), nil, true},
		{"anything into a non-empty interface", int32(1), into[fmt.Stringer](), nil, true},
		{"NaN into float32", math.NaN(), into[float32](), float32(math.NaN()), false},
		{"null into a MinKey", nil, into[MinKey](), MinKey{}, false},
		{"Decimal128 into Decimal128", NewDecimal128(1, 2), into[Decimal128](), NewDecimal128(1, 2), false},
		{"Regex into Regex", Regex{Pattern: "p", Options: "i"}, into[Regex](), Regex{Pattern: "p", Options: "i"}, false},
		{"Timestamp into Timestamp", Timestamp{T: 1, I: 2}, into[Timestamp](), Timestamp{T: 1, I: 2}, false},
	}
}

// namedBytes is a named byte slice, decoded element by element from an
// array as any slice is.
type namedBytes []byte

// decodeInner is a nested document.
type decodeInner struct {
	// X is an integer.
	X int `bson:"x"`
}

// TestDecodeConversions pins which BSON type each Go target accepts.
func TestDecodeConversions(t *testing.T) {
	t.Parallel()
	runCase := func(t *testing.T, c conversion) {
		t.Helper()
		got, err := c.decode(t, oneElement(t, c.bson))
		if c.wantErr {
			if !errs.HasCode(err, CodeBSONUnmarshalFailed) {
				t.Fatalf("Unmarshal = %v (value %#v), want BSON_UNMARSHAL_FAILED", err, got)
			}
			return
		}
		if err != nil {
			t.Fatalf("Unmarshal = %v, want nil", err)
		}
		//: NaN is not equal to itself.
		if f, ok := got.(float32); ok && math.IsNaN(float64(f)) {
			return
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("decoded %#v, want %#v", got, c.want)
		}
	}
	for _, c := range conversionCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDecodeTruncate pins the truncate tag option: a double's fraction is
// dropped into an integer, and a float32 may round.
func TestDecodeTruncate(t *testing.T) {
	t.Parallel()
	type truncated struct {
		// I drops the fraction.
		I int `bson:"i,truncate"`
		// F rounds to float32.
		F float32 `bson:"f,truncate"`
		// Nested inherits the option.
		Nested decodeInner `bson:"n,truncate"`
	}
	data, err := New().Marshal(D{{"i", -2.7}, {"f", 0.1}, {"n", D{{"x", 3.9}}}})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	var got truncated
	if err := New().Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal = %v", err)
	}
	//: toward zero, rounded, and inherited.
	if got.I != -2 || got.F != float32(0.1) || got.Nested.X != 3 {
		t.Errorf("decoded %+v", got)
	}
}

// TestDecodeInterfaceDefaults pins what each BSON type becomes in an interface,
// and which document type a nested document becomes.
func TestDecodeInterfaceDefaults(t *testing.T) {
	t.Parallel()
	oid := ObjectID{9}
	source := D{
		{"double", 1.5},
		{"string", "s"},
		{"doc", D{{"inner", D{{"z", int32(1)}}}}},
		{"array", A{D{{"q", int32(1)}}, nil}},
		{"binary", Binary{Subtype: BinaryUUID, Data: []byte{1}}},
		{"undefined", Undefined{}},
		{"oid", oid},
		{"bool", true},
		{"date", DateTime(5)},
		{"null", nil},
		{"regex", Regex{Pattern: "p", Options: "i"}},
		{"dbp", DBPointer{DB: "d", Pointer: oid}},
		{"js", JavaScript("x")},
		{"symbol", Symbol("y")},
		{"cws", CodeWithScope{Code: "c", Scope: D{{"a", int32(1)}}}},
		{"int32", int32(1)},
		{"ts", Timestamp{T: 1, I: 2}},
		{"int64", int64(1)},
		{"dec", NewDecimal128(1, 2)},
		{"min", MinKey{}},
		{"max", MaxKey{}},
	}
	data, err := New().Marshal(source)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	type tc struct {
		name   string
		decode func() (any, error)
		check  func(t *testing.T, got any)
	}
	tests := []tc{
		{"into *any: D all the way down", func() (any, error) {
			var v any
			err := New().Unmarshal(data, &v)
			return v, err
		}, func(t *testing.T, got any) {
			t.Helper()
			//: the whole value round-trips through D.
			if !reflect.DeepEqual(got, source) {
				t.Errorf("decoded %#v\nwant %#v", got, source)
			}
		}},
		{"into map[string]any: maps all the way down", func() (any, error) {
			var m map[string]any
			err := New().Unmarshal(data, &m)
			return m, err
		}, func(t *testing.T, got any) {
			t.Helper()
			m, _ := got.(map[string]any)
			inner, ok := m["doc"].(map[string]any)
			//: nested documents follow the map.
			if !ok || !reflect.DeepEqual(inner["inner"], map[string]any{"z": int32(1)}) {
				t.Errorf("doc = %#v", m["doc"])
			}
			arr, ok := m["array"].(A)
			//: an array is an A, its documents follow the map too.
			if !ok || !reflect.DeepEqual(arr[0], map[string]any{"q": int32(1)}) {
				t.Errorf("array = %#v", m["array"])
			}
			cws, ok := m["cws"].(CodeWithScope)
			//: a scope is always a D.
			if !ok || !reflect.DeepEqual(cws.Scope, D{{"a", int32(1)}}) {
				t.Errorf("cws = %#v", m["cws"])
			}
		}},
		{"into M: Ms all the way down", func() (any, error) {
			var m M
			err := New().Unmarshal(data, &m)
			return m, err
		}, func(t *testing.T, got any) {
			t.Helper()
			m, _ := got.(M)
			//: the map's own type is the ancestor.
			if _, ok := m["doc"].(M); !ok {
				t.Errorf("doc = %T, want M", m["doc"])
			}
		}},
		{"into a struct's any field: D", func() (any, error) {
			var h decodeHolder[any]
			err := New().Unmarshal(oneElement(t, map[string]any{"k": map[string]any{"n": int32(1)}}), &h)
			return h.V, err
		}, func(t *testing.T, got any) {
			t.Helper()
			want := D{{"k", D{{"n", int32(1)}}}}
			//: a struct field resets the ancestor.
			if !reflect.DeepEqual(got, want) {
				t.Errorf("decoded %#v, want %#v", got, want)
			}
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := c.decode()
		if err != nil {
			t.Fatalf("Unmarshal = %v", err)
		}
		c.check(t, got)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// decodeRecord is a struct with a pointer, a nested struct and an inline map.
type decodeRecord struct {
	// Name is matched exactly, or by the element name lower-cased.
	Name string `bson:"name"`
	// UserName is only matched exactly: lower-casing "username" does not give
	// "userName".
	UserName string `bson:"userName"`
	// Ptr is allocated when a value arrives, set to nil by a null.
	Ptr *int `bson:"ptr"`
	// Kept is not in the document and keeps its value.
	Kept string `bson:"kept"`
	// Inner is inlined.
	Inner decodeInner `bson:",inline"`
	// Extra collects the elements no field names.
	Extra map[string]any `bson:",inline"`
}

// TestDecodeStructs pins how a document fills a struct.
func TestDecodeStructs(t *testing.T) {
	t.Parallel()
	five := 5
	type tc struct {
		name  string
		doc   D
		start decodeRecord
		want  decodeRecord
	}
	tests := []tc{
		{"fields by name, the rest kept", D{{"name", "n"}, {"x", int32(3)}}, decodeRecord{Kept: "k"}, decodeRecord{Name: "n", Kept: "k", Inner: decodeInner{X: 3}}},
		{"an upper-case name falls back to lower case", D{{"NAME", "n"}}, decodeRecord{}, decodeRecord{Name: "n"}},
		{"a mixed-case tag only matches exactly", D{{"username", "u"}}, decodeRecord{}, decodeRecord{Extra: map[string]any{"username": "u"}}},
		{"unknown names go to the inline map", D{{"other", int32(1)}}, decodeRecord{}, decodeRecord{Extra: map[string]any{"other": int32(1)}}},
		{"a value allocates the pointer", D{{"ptr", int32(5)}}, decodeRecord{}, decodeRecord{Ptr: &five}},
		{"null sets the pointer to nil", D{{"ptr", nil}}, decodeRecord{Ptr: &five}, decodeRecord{}},
		{"a repeated name keeps the last value", D{{"name", "a"}, {"name", "b"}}, decodeRecord{}, decodeRecord{Name: "b"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		data, err := New().Marshal(c.doc)
		if err != nil {
			t.Fatalf("fixture: %v", err)
		}
		got := c.start
		if err := New().Unmarshal(data, &got); err != nil {
			t.Fatalf("Unmarshal = %v", err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("decoded %+v, want %+v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// decodeHookValue reads its own value, through a pointer receiver.
type decodeHookValue struct {
	// raw is what UnmarshalBSON received.
	raw []byte
}

// UnmarshalBSON keeps the bytes.
func (h *decodeHookValue) UnmarshalBSON(data []byte) error {
	h.raw = data
	//: accepted.
	return nil
}

// TestDecodeHooks pins UnmarshalBSON: a field gets its element's value bytes,
// a root target the whole document, each a copy.
func TestDecodeHooks(t *testing.T) {
	t.Parallel()
	data, err := New().Marshal(D{{"v", "s"}})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	var field decodeHolder[decodeHookValue]
	if err := New().Unmarshal(data, &field); err != nil {
		t.Fatalf("Unmarshal = %v", err)
	}
	//: the string value's own bytes: length, text, NUL.
	if string(field.V.raw) != "\x02\x00\x00\x00s\x00" {
		t.Errorf("field hook got %q", field.V.raw)
	}
	var root decodeHookValue
	if err := New().Unmarshal(data, &root); err != nil {
		t.Fatalf("Unmarshal = %v", err)
	}
	//: the whole document, as a copy.
	if string(root.raw) != string(data) || &root.raw[0] == &data[0] {
		t.Errorf("root hook got %x", root.raw)
	}
}

// TestDecodeReusesSlices pins the previous library's slice and map handling:
// a slice with room is decoded into in place, a map is added to.
func TestDecodeReusesSlices(t *testing.T) {
	t.Parallel()
	data, err := New().Marshal(D{{"s", A{int32(7)}}, {"m", D{{"b", int32(2)}}}})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	type target struct {
		// S has room for the element.
		S []int `bson:"s"`
		// M already holds a key.
		M map[string]int `bson:"m"`
	}
	backing := make([]int, 1, 4)
	got := target{S: backing, M: map[string]int{"a": 1}}
	if err := New().Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal = %v", err)
	}
	//: the same backing array.
	if &got.S[0] != &backing[0] || got.S[0] != 7 {
		t.Errorf("slice not decoded in place: %v", got.S)
	}
	//: both keys.
	if !reflect.DeepEqual(got.M, map[string]int{"a": 1, "b": 2}) {
		t.Errorf("map = %v", got.M)
	}
}

// TestUnmarshalTargets pins which targets Unmarshal accepts.
func TestUnmarshalTargets(t *testing.T) {
	t.Parallel()
	data, err := New().Marshal(D{{"a", int32(1)}})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	type tc struct {
		name   string
		target func() any
		ok     bool
	}
	tests := []tc{
		{"nil", func() any { return nil }, false},
		{"a value", func() any { return decodeInner{} }, false},
		{"a nil pointer", func() any { return (*decodeInner)(nil) }, false},
		{"a nil map", func() any { return map[string]any(nil) }, false},
		{"a map by value", func() any { return map[string]any{} }, true},
		{"a pointer to a pointer", func() any { return new(*decodeInner) }, true},
		{"a pointer to a string", func() any { return new(string) }, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		target := c.target()
		err := New().Unmarshal(data, target)
		if c.ok != (err == nil) {
			t.Fatalf("Unmarshal(%T) = %v, want ok=%v", target, err, c.ok)
		}
		if err != nil && !errs.HasCode(err, CodeBSONUnmarshalFailed) {
			t.Errorf("Unmarshal(%T) = %v, want BSON_UNMARSHAL_FAILED", target, err)
		}
		//: a map passed by value is filled in place.
		if m, ok := target.(map[string]any); ok && c.ok && m["a"] != int32(1) {
			t.Errorf("the map passed by value holds %v", m)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestMalformedInput pins the validator: each document is refused whole,
// before the target is touched, under the code its fault deserves.
func TestMalformedInput(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		hex  string
		code errs.Code
		rule string
	}
	tests := []tc{
		{"empty input", "", CodeBSONUnmarshalFailed, "shorter than"},
		{"a length shorter than the input", "0500000000ff", CodeBSONUnmarshalFailed, "declared length"},
		{"a length longer than the input", "0600000000", CodeBSONUnmarshalFailed, "declared length"},
		{"no terminator", "0500000001", CodeBSONUnmarshalFailed, "terminator"},
		{"a boolean of 2", "090000000862000200", CodeBSONUnmarshalFailed, "boolean"},
		{"a string length of zero", "0c0000000261000000000000", CodeBSONUnmarshalFailed, "string length"},
		{"a string not terminated where its length ends", "1000000002610004000000616263ff00", CodeBSONUnmarshalFailed, "not terminated"},
		{"a string that is not UTF-8", "0e00000002610002000000e90000", CodeBSONUnmarshalFailed, "UTF-8"},
		{"an element name that is not UTF-8", "0c00000010ff000100000000", CodeBSONUnmarshalFailed, "UTF-8"},
		{"an unknown type byte", "07000000800000", CodeBSONUnmarshalFailed, "unknown type"},
		{"a truncated double", "0b0000000164000000f000", CodeBSONUnmarshalFailed, "truncated"},
		{"a sub-document longer than its parent", "1800000003666f6f000f0000001062617200ffffff7f0000", CodeBSONUnmarshalFailed, "document length"},
		{"an old binary whose inner length disagrees", "13000000057800060000000203000000ffff00", CodeBSONUnmarshalFailed, "subtype 0x02"},
		{"a negative binary length", "0d000000057800ffffffff0000", CodeBSONUnmarshalFailed, "binary length"},
		{"a regex that runs into the terminator", "0c0000000b61006162006300", CodeBSONUnmarshalFailed, "regex is not terminated"},
		{"a code-with-scope too short for its parts", "160000000f61000d0000000100000000050000000000", CodeBSONUnmarshalFailed, "code-with-scope"},
		{"a code-with-scope longer than its parts", "280000000f6100210000000500000061626364001300000010780001000000107900010000000000", CodeBSONUnmarshalFailed, ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		data := mustHex(t, c.hex)
		target := map[string]any{"untouched": true}
		err := New().Unmarshal(data, &target)
		if !errs.HasCode(err, c.code) {
			t.Fatalf("Unmarshal(%s) = %v, want code %v", c.hex, err, c.code)
		}
		//: the target was not touched.
		if len(target) != 1 || target["untouched"] != true {
			t.Errorf("the target changed: %v", target)
		}
		//: the log line names the rule.
		if c.rule != "" && !strings.Contains(privateOf(err), c.rule) {
			t.Errorf("private %q does not name %q", privateOf(err), c.rule)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDecodeNestingBound pins the depth bound on decode: 100 levels, the top
// level included, decode; 101 are refused before any recursion past the
// bound.
func TestDecodeNestingBound(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		levels int
		code   errs.Code
	}
	tests := []tc{
		{"the bound itself", maxBSONNestedLevels, 0},
		{"one past it", maxBSONNestedLevels + 1, CodeBSONDepthExceeded},
		{"an array chain one past it", -(maxBSONNestedLevels + 1), CodeBSONDepthExceeded},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		data := nestedDocument(c.levels)
		var v any
		err := New().Unmarshal(data, &v)
		if c.code == 0 {
			if err != nil {
				t.Fatalf("Unmarshal = %v, want nil", err)
			}
			return
		}
		if !errs.HasCode(err, c.code) {
			t.Fatalf("Unmarshal = %v, want code %v", err, c.code)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// nestedDocument builds, byte by byte, a document nested levels deep — or,
// for a negative count, the same depth of arrays inside a top-level document.
func nestedDocument(levels int) []byte {
	elemType := typeDocument
	//: a negative count asks for arrays.
	if levels < 0 {
		elemType, levels = typeArray, -levels
	}
	doc := []byte{byte(minDocumentSize), 0, 0, 0, 0}
	//: wrap from the innermost outward.
	for range levels - 1 {
		size := lengthSize + 1 + 2 + len(doc) + 1
		next := make([]byte, 0, size)
		next = append(next, byte(size), byte(size>>8), byte(size>>16), byte(size>>24))
		next = append(next, elemType, 'a', 0)
		next = append(next, doc...)
		doc = append(next, 0)
	}
	return doc
}

// mustHex decodes a test vector.
func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	data, err := hex.DecodeString(s)
	//: a malformed vector is a broken test, not a codec failure.
	if err != nil {
		t.Fatalf("vector %q: %v", s, err)
	}
	return data
}

// TestSizeCap pins the input cap: one byte past 10 MiB is refused before any
// byte is read.
func TestSizeCap(t *testing.T) {
	t.Parallel()
	err := New().Unmarshal(make([]byte, maxBSONBytes+1), new(any))
	//: the size sentinel, not a malformation.
	if !errs.HasCode(err, CodeBSONSizeExceeded) {
		t.Fatalf("Unmarshal = %v, want BSON_SIZE_EXCEEDED", err)
	}
}
