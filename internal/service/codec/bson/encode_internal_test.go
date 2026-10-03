// Package bson — the encoder against byte-exact expectations. The vectors were
// produced by the MongoDB Go driver v1.17.9, the library this codec replaced,
// and checked against this encoder in a differential run before the driver
// left the module graph; they pin the wire format a stored document already
// has.
package bson

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// encodeTagged exercises the struct tag options.
type encodeTagged struct {
	// Name is renamed by its tag.
	Name string `bson:"name"`
	// Count is left out when zero.
	Count int `bson:"count,omitempty"`
	// Small is an int64 that minsize narrows.
	Small int64 `bson:"small,minsize"`
	// Wide is an int64 that minsize cannot narrow.
	Wide int64 `bson:"wide,minsize"`
	// Skipped is never written.
	Skipped string `bson:"-"`
	// Dash is named "-" by the dash-comma form.
	Dash string `bson:"-,"`
}

// encodeInner is an inlined struct.
type encodeInner struct {
	// X is promoted by the inline.
	X int `bson:"x"`
	// Y is promoted by the inline.
	Y string `bson:"y"`
}

// encodeInline flattens a struct and collects extra keys in a map.
type encodeInline struct {
	// A comes first.
	A int `bson:"a"`
	// Inner is flattened into this document.
	Inner encodeInner `bson:",inline"`
	// Rest holds every other key, written last.
	Rest map[string]any `bson:",inline"`
}

// encodeInlinePointer flattens a struct through a pointer.
type encodeInlinePointer struct {
	// A comes first.
	A int `bson:"a"`
	// Inner is flattened when not nil.
	Inner *encodeInner `bson:",inline"`
}

// encodeShadow has a field that shadows an inlined one.
type encodeShadow struct {
	// X wins over the inlined X: it is shallower.
	X int `bson:"x"`
	// Inner contributes Y only.
	Inner encodeInner `bson:",inline"`
}

// encodeDuplicate has two fields claiming one name at the same depth.
type encodeDuplicate struct {
	// A claims "k".
	A int `bson:"k"`
	// B claims "k" too.
	B int `bson:"k"`
}

// EncodeEmbedded is embedded without ",inline", so it is a sub-document.
type EncodeEmbedded struct {
	// E1 is a field of the embedded struct.
	E1 int
}

// encodeWithEmbedded embeds a struct without inlining it.
type encodeWithEmbedded struct {
	EncodeEmbedded
	// Z follows it.
	Z int
}

// encodeValueHook writes its own document through a value receiver.
type encodeValueHook struct {
	// n is written as "v".
	n int32
}

// MarshalBSON writes {"v": n}.
func (h encodeValueHook) MarshalBSON() ([]byte, error) {
	//: a fixed one-element document.
	return New().Marshal(map[string]int32{"v": h.n})
}

// encodePointerHook writes its own document through a pointer receiver.
type encodePointerHook struct {
	// N is written as "p" by the hook, or as "n" when the hook is unreachable.
	N int32 `bson:"n"`
}

// MarshalBSON writes {"p": N}.
func (h *encodePointerHook) MarshalBSON() ([]byte, error) {
	//: a fixed one-element document.
	return New().Marshal(map[string]int32{"p": h.N})
}

// encodeHooks holds the two hooks.
type encodeHooks struct {
	// V uses the value receiver.
	V encodeValueHook `bson:"v"`
	// P uses the pointer receiver when addressable.
	P encodePointerHook `bson:"p"`
}

// encodeZeroer is empty when its IsZero says so.
type encodeZeroer struct {
	// N is the value; 42 counts as empty.
	N int
}

// IsZero reports 42 as empty.
func (z encodeZeroer) IsZero() bool {
	//: an arbitrary sentinel, to tell IsZero from the zero value.
	return z.N == 42
}

// encodeOmitted holds every omitempty shape.
type encodeOmitted struct {
	// Z is judged by IsZero.
	Z encodeZeroer `bson:"z,omitempty"`
	// When is judged by time.Time.IsZero.
	When time.Time `bson:"when,omitempty"`
	// P is empty when nil.
	P *int `bson:"p,omitempty"`
	// Any is empty only when nil.
	Any any `bson:"any,omitempty"`
	// S is empty when it has no element.
	S []int `bson:"s,omitempty"`
	// St is a struct without IsZero, never empty.
	St encodeInner `bson:"st,omitempty"`
}

// encodeTextKey is a map key written through MarshalText.
type encodeTextKey struct {
	// a and b are joined by a dash.
	a, b string
}

// MarshalText joins the halves.
func (k encodeTextKey) MarshalText() ([]byte, error) {
	//: "a-b".
	return []byte(k.a + "-" + k.b), nil
}

// encodeCycle points at itself.
type encodeCycle struct {
	// Next closes the cycle.
	Next *encodeCycle `bson:"next"`
}

// goldenEncode is one value and the bytes it must encode to.
type goldenEncode struct {
	name  string
	value any
	want  string
}

// goldenEncodeCases are the byte-exact encodings.
func goldenEncodeCases() []goldenEncode {
	one := 1
	when := time.Date(2026, time.January, 2, 3, 4, 5, 678_901_234, time.UTC)
	cases := []goldenEncode{
		{"the empty document", map[string]any{}, "0500000000"},
		{"a nil map at the top level is the empty document", map[string]any(nil), "0500000000"},
		{"int32", map[string]int32{"a": 1}, "0c0000001061000100000000"},
		{"int narrows when it fits", map[string]int{"a": 1}, "0c0000001061000100000000"},
		{"int8 is an int32", map[string]int8{"a": -3}, "0c000000106100fdffffff00"},
		{"int64 stays an int64", map[string]int64{"a": 1}, "10000000126100010000000000000000"},
		{"uint8 is an int32", map[string]uint8{"a": 200}, "0c000000106100c800000000"},
		{"uint is an int64", map[string]uint{"a": 3}, "10000000126100030000000000000000"},
		{"uint64 at the int64 bound", map[string]uint64{"a": math.MaxInt64}, "10000000126100ffffffffffffff7f00"},
		{"float64", map[string]float64{"a": 1.5}, "10000000016100000000000000f83f00"},
		{"float32 widens exactly", map[string]float32{"a": 1.5}, "10000000016100000000000000f83f00"},
		{"bool", map[string]bool{"a": true}, "090000000861000100"},
		{"string", map[string]string{"a": "b"}, "0e00000002610002000000620000"},
		{"a string may hold a NUL", map[string]string{"a": "b\x00c"}, "10000000026100040000006200630000"},
		{"bytes are a generic binary", map[string][]byte{"a": {1, 2}}, "0f0000000561000200000000010200"},
		{"nil bytes are null", map[string][]byte{"a": nil}, "080000000a610000"},
		{"a byte array is a binary", map[string][2]byte{"a": {9, 8}}, "0f0000000561000200000000090800"},
		{"a slice is an array", map[string][]int{"a": {1, 2}}, "1b0000000461001300000010300001000000103100020000000000"},
		{"a nil slice is null", map[string][]int{"a": nil}, "080000000a610000"},
		{"an int array", map[string][2]int{"a": {1, 2}}, "1b0000000461001300000010300001000000103100020000000000"},
		{"a time is a UTC datetime in milliseconds", map[string]time.Time{"a": when}, "100000000961002e8fa97c9b01000000"},
		{"a nil pointer is null", map[string]*int{"a": nil}, "080000000a610000"},
		{"a pointer is its target", map[string]*int{"a": &one}, "0c0000001061000100000000"},
		{"a nil interface is null", map[string]any{"a": nil}, "080000000a610000"},
		{"map keys are sorted", map[string]int32{"b": 2, "a": 1, "c": 3}, "1a00000010610001000000106200020000001063000300000000"},
		{"an int map key in decimal", map[int]int32{-5: 1}, "0d000000102d35000100000000"},
		{"a text map key", map[encodeTextKey]int32{{"x", "y"}: 1}, "0e00000010782d79000100000000"},
		{"a D keeps its order", D{{"z", int32(1)}, {"a", "s"}}, "15000000107a000100000002610002000000730000"},
		{"an M is a map", M{"a": int32(1)}, "0c0000001061000100000000"},
		{"an A is an array", map[string]any{"a": A{int32(1)}}, "140000000461000c000000103000010000000000"},
		{"a struct with its tags", encodeTagged{Name: "n", Small: 7, Wide: 1 << 40, Skipped: "s", Dash: "d"}, "33000000026e616d6500020000006e0010736d616c6c00070000001277696465000000000000010000022d0002000000640000"},
		{"inline struct and map", encodeInline{A: 1, Inner: encodeInner{X: 2, Y: "y"}, Rest: map[string]any{"r": "s"}}, "25000000106100010000001078000200000002790002000000790002720002000000730000"},
		{"a nil inline pointer contributes nothing", encodeInlinePointer{A: 1}, "0c0000001061000100000000"},
		{"a shallower field shadows an inlined one", encodeShadow{X: 1, Inner: encodeInner{X: 2, Y: "y"}}, "150000001078000100000002790002000000790000"},
		{"an embedded struct without inline is a sub-document", encodeWithEmbedded{EncodeEmbedded{1}, 2}, "2900000003656e636f6465656d626564646564000d000000106531000100000000107a000200000000"},
		{"MarshalBSON by value, and by pointer through an address", &encodeHooks{V: encodeValueHook{3}, P: encodePointerHook{4}}, "230000000376000c00000010760003000000000370000c000000107000040000000000"},
		{"MarshalBSON by pointer is not reached without an address", encodeHooks{V: encodeValueHook{3}, P: encodePointerHook{4}}, "230000000376000c00000010760003000000000370000c000000106e00040000000000"},
		{"omitempty, each rule", encodeOmitted{Z: encodeZeroer{N: 42}, S: []int{}}, "1d00000003737400140000001078000000000002790001000000000000"},
		{"an ObjectID", map[string]ObjectID{"a": {1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}}, "140000000761000102030405060708090a0b0c00"},
		{"a DateTime", map[string]DateTime{"a": -1500}, "1000000009610024faffffffffffff00"},
		{"a Binary keeps its subtype", map[string]Binary{"a": {Subtype: BinaryUUID, Data: []byte{1}}}, "0e00000005610001000000040100"},
		{"an old Binary nests its length", map[string]Binary{"a": {Subtype: BinaryOld, Data: []byte{1, 2}}}, "13000000056100060000000202000000010200"},
		{"a Regex sorts its options", map[string]Regex{"a": {Pattern: "p", Options: "xmi"}}, "0e0000000b61007000696d780000"},
		{"a Timestamp, increment first", map[string]Timestamp{"a": {T: 1, I: 2}}, "10000000116100020000000100000000"},
		{"a Decimal128, low half first", map[string]Decimal128{"a": NewDecimal128(0x3040000000000000, 1)}, "180000001361000100000000000000000000000000403000"},
		{"the markers", D{{"a", MinKey{}}, {"b", MaxKey{}}, {"c", Undefined{}}, {"d", Null{}}}, "11000000ff61007f62000663000a640000"},
		{"JavaScript and Symbol", D{{"a", JavaScript("x")}, {"b", Symbol("y")}}, "170000000d61000200000078000e620002000000790000"},
		{"a DBPointer", map[string]DBPointer{"a": {DB: "d", Pointer: ObjectID{1}}}, "1a0000000c610002000000640001000000000000000000000000"},
		{"a CodeWithScope", map[string]CodeWithScope{"a": {Code: "f", Scope: D{{"x", int32(1)}}}}, "1e0000000f6100160000000200000066000c000000107800010000000000"},
		{"a json.Number integer", map[string]json.Number{"a": "12"}, "100000001261000c0000000000000000"},
		{"a json.Number float", map[string]json.Number{"a": "1.5"}, "10000000016100000000000000f83f00"},
		{"a url.URL is its string", map[string]url.URL{"a": {Scheme: "https", Host: "x.y"}}, "180000000261000c00000068747470733a2f2f782e790000"},
	}
	//: an int past int32 exists only where int is 64 bits wide.
	if strconv.IntSize == 64 {
		var wide int64 = 1 << 40
		cases = append(cases, goldenEncode{"int widens when it does not", map[string]int{"a": int(wide)}, "10000000126100000000000001000000"})
	}
	return cases
}

// TestGoldenEncodings pins every mapping to the bytes the previous library
// wrote for it.
func TestGoldenEncodings(t *testing.T) {
	t.Parallel()
	runCase := func(t *testing.T, c goldenEncode) {
		t.Helper()
		got, err := New().Marshal(c.value)
		if err != nil {
			t.Fatalf("Marshal = %v, want nil", err)
		}
		//: byte for byte.
		if hex.EncodeToString(got) != c.want {
			t.Errorf("Marshal =\n  %x\nwant\n  %s", got, c.want)
		}
		//: and the bytes are one well-formed document.
		if err := validateRoot(got); err != nil {
			t.Errorf("the encoding does not validate: %v", err)
		}
	}
	for _, c := range goldenEncodeCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestBareTag pins the previous library's reading of a struct tag with no key
// and no colon at all as a bson tag, and the lower-cased field name a field
// without a bson tag gets. go vet refuses a bare tag in source, so the struct
// is built at run time.
func TestBareTag(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		tag  reflect.StructTag
		want string
	}
	tests := []tc{
		{"a bare tag renames", `renamed`, "1072656e616d6564000500000000"},
		{"a keyed tag of another codec does not", `json:"other"`, "1062617265000500000000"},
		{"no tag at all lower-cases the field name", "", "1062617265000500000000"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		typ := reflect.StructOf([]reflect.StructField{{Name: "Bare", Type: reflect.TypeFor[int](), Tag: c.tag}})
		value := reflect.New(typ).Elem()
		value.Field(0).SetInt(5)
		got, err := New().Marshal(value.Interface())
		if err != nil {
			t.Fatalf("Marshal = %v", err)
		}
		//: the element alone, after the length prefix.
		if body := hex.EncodeToString(got[lengthSize:]); body != c.want {
			t.Errorf("Marshal = %s, want %s", body, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestMarshalRefusals pins what Marshal refuses, and under which code.
func TestMarshalRefusals(t *testing.T) {
	t.Parallel()
	cycle := &encodeCycle{}
	cycle.Next = cycle
	selfMap := map[string]any{}
	selfMap["self"] = selfMap
	type tc struct {
		name  string
		value any
		code  errs.Code
		in    string
	}
	tests := []tc{
		{"a scalar at the top level", 42, CodeBSONMarshalFailed, "top level must be a document"},
		{"a slice at the top level", []int{1}, CodeBSONMarshalFailed, "top level must be a document"},
		{"a nil D at the top level", D(nil), CodeBSONMarshalFailed, "top level must be a document"},
		{"a nil pointer at the top level", (*encodeTagged)(nil), CodeBSONMarshalFailed, "top level must be a document"},
		{"nil", nil, CodeBSONMarshalFailed, "nil value"},
		{"a channel", map[string]any{"c": make(chan int)}, CodeBSONMarshalFailed, "no BSON form"},
		{"a function", map[string]any{"f": func() {}}, CodeBSONMarshalFailed, "no BSON form"},
		{"a complex number", map[string]complex64{"c": 1}, CodeBSONMarshalFailed, "no BSON form"},
		{"a uint64 past int64", map[string]uint64{"u": math.MaxInt64 + 1}, CodeBSONMarshalFailed, "overflows int64"},
		{"a key holding a NUL", map[string]int{"a\x00b": 1}, CodeBSONMarshalFailed, "NUL"},
		{"a string that is not UTF-8", map[string]string{"s": "\xff"}, CodeBSONMarshalFailed, "UTF-8"},
		{"a key that is not UTF-8", map[string]int{"\xff": 1}, CodeBSONMarshalFailed, "UTF-8"},
		{"a regex holding a NUL", map[string]Regex{"r": {Pattern: "a\x00"}}, CodeBSONMarshalFailed, "regex"},
		{"a float map key", map[float64]int{1.5: 1}, CodeBSONMarshalFailed, "element-name form"},
		{"an inline key colliding with a field", encodeInline{Rest: map[string]any{"x": 1}}, CodeBSONMarshalFailed, "collides"},
		{"two fields with one name", encodeDuplicate{}, CodeBSONMarshalFailed, "two fields named k"},
		{"a json.Number that is not a number", map[string]json.Number{"n": "abc"}, CodeBSONMarshalFailed, "json.Number"},
		{"a CodeWithScope without a scope", map[string]CodeWithScope{"c": {Code: "f"}}, CodeBSONMarshalFailed, "nil scope"},
		{"a CodeWithScope whose scope is a scalar", map[string]CodeWithScope{"c": {Code: "f", Scope: 5}}, CodeBSONMarshalFailed, "not a document"},
		{"a pointer cycle", cycle, CodeBSONDepthExceeded, "nesting"},
		{"a map that contains itself", selfMap, CodeBSONDepthExceeded, "nesting"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := New().Marshal(c.value)
		if !errs.HasCode(err, c.code) {
			t.Fatalf("Marshal = %v, want code %v", err, c.code)
		}
		//: a refusal produces no partial output.
		if got != nil {
			t.Errorf("Marshal returned %x beside the error", got)
		}
		private := privateOf(err)
		//: the log line says why.
		if !strings.Contains(private, c.in) {
			t.Errorf("the private message %q does not mention %q", private, c.in)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestNestingBound pins the depth bound on encode: the top level counts as
// the first level, so 100 levels are written and 101 are refused.
func TestNestingBound(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		levels int
		ok     bool
	}
	tests := []tc{
		{"the bound itself", maxBSONNestedLevels, true},
		{"one past it", maxBSONNestedLevels + 1, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var value any = map[string]any{}
		//: wrap until the requested depth, the top level included.
		for range c.levels - 1 {
			value = map[string]any{"a": value}
		}
		data, err := New().Marshal(value)
		if c.ok {
			if err != nil {
				t.Fatalf("Marshal at %d levels = %v, want nil", c.levels, err)
			}
			//: the decoder accepts what the encoder wrote.
			var back map[string]any
			if uerr := New().Unmarshal(data, &back); uerr != nil {
				t.Fatalf("Unmarshal at %d levels = %v, want nil", c.levels, uerr)
			}
			return
		}
		if !errs.HasCode(err, CodeBSONDepthExceeded) {
			t.Fatalf("Marshal at %d levels = %v, want BSON_DEPTH_EXCEEDED", c.levels, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestMarshalIsDeterministic pins that the same map encodes to the same bytes
// every time: the order is the keys', not the map's.
func TestMarshalIsDeterministic(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		value any
	}
	flat := map[string]int{}
	//: seven keys, so a randomised iteration order shows.
	for i, key := range []string{"e", "d", "c", "b", "a", "f", "g"} {
		flat[key] = i
	}
	tests := []tc{
		{"a flat map", flat},
		{"nested maps", map[string]any{"z": map[string]any{"y": 1, "x": 2}, "a": []any{map[string]any{"q": 1, "p": 2}}}},
		{"an inline map", encodeInline{Rest: map[string]any{"r3": 3, "r1": 1, "r2": 2}}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		first, err := New().Marshal(c.value)
		if err != nil {
			t.Fatalf("Marshal = %v", err)
		}
		//: many tries, since a map's iteration order is randomised per range.
		for range 50 {
			again, err := New().Marshal(c.value)
			if err != nil || string(again) != string(first) {
				t.Fatalf("Marshal gave %x then %x (%v)", first, again, err)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// privateOf returns an SDK error's log-only message.
func privateOf(err error) string {
	//: the accessor walks the chain.
	return errs.PrivateOf(err)
}
