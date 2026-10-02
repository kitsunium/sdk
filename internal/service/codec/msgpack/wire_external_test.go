package msgpack_test

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"math"
	"net"
	"net/netip"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/codec/msgpack"
)

// goldenPath is the file holding the wire bytes the vendor-backed codec
// (github.com/vmihailenco/msgpack/v5 v5.4.1, through this package's Marshal
// with UseCompactInts) produced for every vector below, one line each:
//
//	<name> TAB <hex of Marshal(value)> TAB <renderAny(Unmarshal(bytes, *any))>
//
// It was generated ONCE, before the native implementation replaced the
// vendor, and is never regenerated from the native codec: the point of the
// file is that the two implementations agree byte for byte, so it must keep
// recording what the vendor wrote.
const goldenPath string = "testdata/vendor-golden.txt"

// Integer boundaries as typed constants, so the vectors compile on 32-bit
// platforms where an untyped int cannot hold them.
const (
	// wireMaxUint32 is the largest uint 32.
	wireMaxUint32 int64 = math.MaxUint32
	// wireMaxUint32Plus1 is the smallest uint 64 the encoder chooses.
	wireMaxUint32Plus1 int64 = math.MaxUint32 + 1
	// wireMaxInt64 is the largest int64, written as a uint 64.
	wireMaxInt64 int64 = math.MaxInt64
	// wireMinInt32Minus1 is the largest int 64 the encoder chooses.
	wireMinInt32Minus1 int64 = math.MinInt32 - 1
	// wireMinInt64 is the smallest int 64.
	wireMinInt64 int64 = math.MinInt64
	// wireMaxUint64 is the largest uint 64.
	wireMaxUint64 uint64 = math.MaxUint64
)

// wireVector is one value whose MessagePack encoding is pinned.
type wireVector struct {
	// name identifies the vector in the golden file and in failures.
	name string
	// value is what Marshal encodes.
	value any
	// want is the value a typed decode of the bytes yields when it is not
	// value itself (a field the encoder leaves out comes back zero).
	want any
	// noDecode skips the typed round trip, for values whose decoded form is
	// equal in meaning but not under reflect.DeepEqual (an error value).
	noDecode bool
}

// Fixture types — each one exercises one rule of the struct mapping.
type (
	// wireTagged names its fields with msgpack tags.
	wireTagged struct {
		Name string `msgpack:"name"`
		Age  int    `msgpack:"age"`
	}
	// wireUntagged has no tags, so the Go field names are the keys.
	wireUntagged struct {
		A int
		B string
	}
	// wireOmit puts omitempty on every family isEmpty distinguishes.
	wireOmit struct {
		A int            `msgpack:"a,omitempty"`
		B string         `msgpack:"b,omitempty"`
		C []int          `msgpack:"c,omitempty"`
		D *int           `msgpack:"d,omitempty"`
		E time.Time      `msgpack:"e,omitempty"`
		F map[string]int `msgpack:"f,omitempty"`
		G bool           `msgpack:"g,omitempty"`
		H float64        `msgpack:"h,omitempty"`
		K int            `msgpack:"k"`
	}
	// wireSkip has a "-" field and an unexported one, both left out.
	wireSkip struct {
		A int `msgpack:"-"`
		B int `msgpack:"b"`
		c int
	}
	// wireArray is encoded as an array through the _msgpack marker field.
	wireArray struct {
		_msgpack struct{} `msgpack:",as_array"` //nolint:unused // read by the codec through its tag
		A        int      `msgpack:"A"`
		B        string   `msgpack:"B"`
	}
	// wireAllOmit sets omitempty for every field through the marker field.
	wireAllOmit struct {
		_msgpack struct{} `msgpack:",omitempty"` //nolint:unused // read by the codec through its tag
		A        int      `msgpack:"A"`
		B        string   `msgpack:"B"`
	}
	// wireEmbedded inlines its embedded struct's fields.
	wireEmbedded struct {
		wireUntagged
		C bool
	}
	// WireBase is an EXPORTED struct, so a decoder may allocate it when it
	// is embedded through a nil pointer.
	WireBase struct {
		A int
		B string
	}
	// wireEmbeddedPtr inlines through a pointer.
	wireEmbeddedPtr struct {
		*WireBase
		C bool
	}
	// wireNoInline keeps its embedded struct as one nested field.
	wireNoInline struct {
		wireUntagged `msgpack:",noinline"`
		C            bool `msgpack:"C"`
	}
	// wireShadow cannot inline: its own A would collide with the embedded A.
	wireShadow struct {
		A int
		wireUntagged
	}
	// wireAlias decodes "aa" into A but encodes "a".
	wireAlias struct {
		A int `msgpack:"a,alias:aa"`
	}
	// wireNested nests the tagged struct three ways.
	wireNested struct {
		Inner wireTagged   `msgpack:"inner"`
		List  []wireTagged `msgpack:"list"`
		Ptr   *wireTagged  `msgpack:"ptr"`
		Nil   *wireTagged  `msgpack:"nil"`
	}
	// wireTimes carries a time by value and by pointer.
	wireTimes struct {
		T time.Time  `msgpack:"t"`
		P *time.Time `msgpack:"p"`
	}
	// wireErr carries an error interface, encoded as its message.
	wireErr struct {
		E error `msgpack:"e"`
	}
	// wireIface carries an interface field.
	wireIface struct {
		V any `msgpack:"v"`
	}
	// wireMyString is a named string.
	wireMyString string
	// wireMyBytes is a named byte slice.
	wireMyBytes []byte
	// wireMyInt is a named integer.
	wireMyInt int16
	// wireByte is a named byte, so []wireByte is still a byte slice.
	wireByte byte
	// wireText implements encoding.TextMarshaler and TextUnmarshaler.
	wireText struct {
		S string
	}
	// wireBin implements encoding.BinaryMarshaler and BinaryUnmarshaler.
	wireBin struct {
		B [2]byte
	}
)

// MarshalText renders the text form.
func (w *wireText) MarshalText() ([]byte, error) { return []byte("text:" + w.S), nil }

// UnmarshalText parses the text form.
func (w *wireText) UnmarshalText(b []byte) error {
	s, ok := strings.CutPrefix(string(b), "text:")
	if !ok {
		return errors.New("wireText: missing prefix")
	}
	w.S = s
	return nil
}

// MarshalBinary renders the binary form.
func (w *wireBin) MarshalBinary() ([]byte, error) { return []byte{0xb1, w.B[0], w.B[1]}, nil }

// UnmarshalBinary parses the binary form.
func (w *wireBin) UnmarshalBinary(b []byte) error {
	if len(b) != 3 || b[0] != 0xb1 {
		return errors.New("wireBin: bad form")
	}
	w.B = [2]byte{b[1], b[2]}
	return nil
}

// wireVectors is the vector table. Every value's encoding is deterministic:
// no map carries more than one key, because Go randomises map iteration and
// so does MessagePack's map order.
func wireVectors() []wireVector {
	seven := 7
	when := time.Date(2024, time.January, 15, 9, 30, 0, 0, time.UTC)
	return slices.Concat(wireScalarVectors(), wireTimeVectors(), []wireVector{
		{name: "bytes-nil", value: []byte(nil)},
		{name: "bytes-empty", value: []byte{}},
		{name: "bytes-3", value: []byte{1, 2, 3}},
		{name: "byte-array", value: [3]byte{1, 2, 3}},
		{name: "named-bytes", value: wireMyBytes{9, 8}},
		{name: "named-byte-elem", value: []wireByte{4, 5}},
		{name: "ints", value: []int{1, 2, 3}},
		{name: "ints-nil", value: []int(nil)},
		{name: "ints-empty", value: []int{}},
		{name: "int-array", value: [2]int{1, -2}},
		{name: "int8s", value: []int8{-1, 2}},
		{name: "strings", value: []string{"a", "bc"}},
		{name: "anys", value: []any{nil, true, "x", 1.5}},
		{name: "array16", value: make([]bool, 16)},
		{name: "map-string-int", value: map[string]int{"a": 1}},
		{name: "map-int-string", value: map[int]string{-3: "x"}},
		{name: "map-nil", value: map[string]int(nil)},
		{name: "map-empty", value: map[string]bool{}},
		{name: "map-string-any", value: map[string]any{"k": []any{"x", false}}},
		{name: "map-string-string", value: map[string]string{"k": "v"}},
		{name: "struct-tagged", value: wireTagged{Name: "a", Age: 1}},
		{name: "struct-untagged", value: wireUntagged{A: 300, B: "b"}},
		{name: "struct-omit-empty", value: wireOmit{}},
		{name: "struct-omit-full", value: wireOmit{A: 1, B: "b", C: []int{1}, D: &seven, E: when, F: map[string]int{"f": 2}, G: true, H: 0.5, K: 3}},
		{name: "struct-skip", value: wireSkip{A: 1, B: 2}, want: wireSkip{B: 2}},
		{name: "struct-as-array", value: wireArray{A: 5, B: "five"}},
		{name: "struct-all-omit", value: wireAllOmit{B: "only"}},
		{name: "struct-embedded", value: wireEmbedded{wireUntagged: wireUntagged{A: 1, B: "b"}, C: true}},
		{name: "struct-embedded-ptr", value: wireEmbeddedPtr{WireBase: &WireBase{A: 2, B: "p"}, C: true}},
		{name: "struct-embedded-ptr-nil", value: wireEmbeddedPtr{C: true}, want: wireEmbeddedPtr{WireBase: &WireBase{}, C: true}},
		{name: "struct-noinline", value: wireNoInline{wireUntagged: wireUntagged{A: 3}, C: false}},
		{name: "struct-shadow", value: wireShadow{A: 9, wireUntagged: wireUntagged{A: 4, B: "s"}}},
		{name: "struct-alias", value: wireAlias{A: 6}},
		{name: "struct-nested", value: wireNested{Inner: wireTagged{Name: "i"}, List: []wireTagged{{Age: 2}}, Ptr: &wireTagged{Name: "p", Age: -1}}},
		{name: "struct-times", value: wireTimes{T: when, P: &when}},
		{name: "struct-error", value: wireErr{E: errors.New("boom")}, noDecode: true},
		{name: "struct-error-nil", value: wireErr{}},
		{name: "struct-iface", value: wireIface{V: "s"}},
		{name: "named-string", value: wireMyString("named")},
		{name: "named-int", value: wireMyInt(-300)},
		{name: "text-marshaler", value: &wireText{S: "hi"}},
		{name: "binary-marshaler", value: &wireBin{B: [2]byte{7, 8}}},
		{name: "netip-addr", value: netip.MustParseAddr("192.0.2.1")},
		{name: "net-ip", value: net.ParseIP("192.0.2.1")},
		{name: "ptr-int", value: &seven},
		{name: "ptr-ptr-int", value: new(&seven)},
		{name: "duration", value: 1500 * time.Millisecond},
	})
}

// wireScalarVectors covers nil, booleans, every integer boundary of the
// format families, floats and string lengths.
func wireScalarVectors() []wireVector {
	return []wireVector{
		{name: "nil", value: nil},
		{name: "true", value: true},
		{name: "false", value: false},
		{name: "int-0", value: 0},
		{name: "int-127", value: 127},
		{name: "int-128", value: 128},
		{name: "int-255", value: 255},
		{name: "int-256", value: 256},
		{name: "int-65535", value: 65535},
		{name: "int-65536", value: 65536},
		{name: "int-maxuint32", value: wireMaxUint32},
		{name: "int-maxuint32+1", value: wireMaxUint32Plus1},
		{name: "int-maxint64", value: wireMaxInt64},
		{name: "int-minus1", value: -1},
		{name: "int-minus32", value: -32},
		{name: "int-minus33", value: -33},
		{name: "int-minus128", value: -128},
		{name: "int-minus129", value: -129},
		{name: "int-minus32768", value: -32768},
		{name: "int-minus32769", value: -32769},
		{name: "int-minint32", value: math.MinInt32},
		{name: "int-minint32-1", value: wireMinInt32Minus1},
		{name: "int-minint64", value: wireMinInt64},
		{name: "int8", value: int8(-8)},
		{name: "int8-pos", value: int8(100)},
		{name: "int16", value: int16(-1600)},
		{name: "int16-small", value: int16(5)},
		{name: "int32", value: int32(-32_000_000)},
		{name: "int64", value: int64(-64_000_000_000)},
		{name: "int64-small", value: int64(3)},
		{name: "uint", value: uint(7)},
		{name: "uint8", value: uint8(200)},
		{name: "uint16", value: uint16(60_000)},
		{name: "uint32", value: uint32(4_000_000_000)},
		{name: "uint64", value: uint64(9_000_000_000)},
		{name: "uint64-max", value: wireMaxUint64},
		{name: "float32", value: float32(3.1415927)},
		{name: "float32-whole", value: float32(2)},
		{name: "float64", value: 2.718281828459045},
		{name: "float64-zero", value: 0.0},
		{name: "float64-negzero", value: math.Copysign(0, -1)},
		{name: "float64-inf", value: math.Inf(-1)},
		{name: "str-empty", value: ""},
		{name: "str-1", value: "a"},
		{name: "str-31", value: strings.Repeat("s", 31)},
		{name: "str-32", value: strings.Repeat("s", 32)},
		{name: "str-255", value: strings.Repeat("s", 255)},
		{name: "str-256", value: strings.Repeat("s", 256)},
		{name: "str-utf8", value: "héllo 世界 🌸"},
	}
}

// wireTimeVectors covers every timestamp form the encoder chooses between.
func wireTimeVectors() []wireVector {
	return []wireVector{
		{name: "time-epoch", value: time.Unix(0, 0).UTC()},
		{name: "time-32", value: time.Unix(1, 0).UTC()},
		{name: "time-32-max", value: time.Unix(math.MaxUint32, 0).UTC()},
		{name: "time-64", value: time.Unix(1, 500).UTC()},
		{name: "time-64-max", value: time.Unix(1<<34-1, 999_999_999).UTC()},
		{name: "time-96", value: time.Unix(1<<34, 0).UTC()},
		{name: "time-96-negative", value: time.Unix(-1, 0).UTC()},
		{name: "time-96-negative-nanos", value: time.Unix(-2, 1).UTC()},
		{name: "time-zero", value: time.Time{}},
		{name: "time-fixture", value: time.Date(2024, time.January, 15, 9, 30, 0, 0, time.UTC)},
	}
}

// renderAny prints a value decoded into any with its dynamic types, so the
// golden file pins WHICH Go type each wire family decodes to and not only
// the value. Maps print their keys in sorted order, and a time.Time prints
// in UTC: the instant is what the wire carries, and the location a decoder
// attaches is checked by its own test.
func renderAny(v any) string {
	switch x := v.(type) {
	case nil:
		return "nil"
	case time.Time:
		return "time.Time(" + x.UTC().Format(time.RFC3339Nano) + ")"
	case []byte:
		return fmt.Sprintf("[]byte(%x)", x)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = renderAny(e)
		}
		return "[" + strings.Join(parts, " ") + "]"
	case map[string]any:
		keys := slices.Sorted(maps.Keys(x))
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = fmt.Sprintf("%q:%s", k, renderAny(x[k]))
		}
		return "{" + strings.Join(parts, " ") + "}"
	default:
		return fmt.Sprintf("%T(%v)", x, x)
	}
}

// goldenLine is one parsed line of the golden file.
type goldenLine struct {
	// wire is the vendor's encoding.
	wire []byte
	// asAny is renderAny of the vendor's decode into any, or "ERROR".
	asAny string
}

// readGolden parses the golden file into name → line.
func readGolden(t *testing.T) map[string]goldenLine {
	t.Helper()
	f, err := os.Open(goldenPath)
	if err != nil {
		t.Fatalf("open %s: %v", goldenPath, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("close %s: %v", goldenPath, cerr)
		}
	}()
	out := map[string]goldenLine{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.SplitN(line, "\t", 3)
		if len(cols) != 3 {
			t.Fatalf("malformed golden line %q", line)
		}
		wire, herr := hex.DecodeString(cols[1])
		if herr != nil {
			t.Fatalf("golden %s: bad hex: %v", cols[0], herr)
		}
		out[cols[0]] = goldenLine{wire: wire, asAny: cols[2]}
	}
	if serr := sc.Err(); serr != nil {
		t.Fatalf("scan %s: %v", goldenPath, serr)
	}
	return out
}

// TestWireGolden pins the native codec to the bytes the vendor-backed codec
// wrote for every vector: Marshal must reproduce them exactly, Unmarshal must
// read them back into the value's own type, and an untyped decode must yield
// the same Go types the vendor yielded.
func TestWireGolden(t *testing.T) {
	t.Parallel()
	golden := readGolden(t)
	c := msgpack.New()
	for _, vec := range wireVectors() {
		t.Run(vec.name, func(t *testing.T) {
			t.Parallel()
			want, ok := golden[vec.name]
			if !ok {
				t.Fatalf("no golden line for %q", vec.name)
			}
			got, err := c.Marshal(vec.value)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if !slices.Equal(got, want.wire) {
				t.Fatalf("Marshal:\n got  %x\n want %x", got, want.wire)
			}
			var asAny any
			derr := c.Unmarshal(want.wire, &asAny)
			switch {
			case want.asAny == "ERROR" && derr == nil:
				t.Errorf("Unmarshal into any: want an error, got %s", renderAny(asAny))
			case want.asAny != "ERROR" && derr != nil:
				t.Fatalf("Unmarshal into any: %v", derr)
			case derr == nil && renderAny(asAny) != want.asAny:
				t.Errorf("Unmarshal into any:\n got  %s\n want %s", renderAny(asAny), want.asAny)
			}
			if vec.noDecode || vec.value == nil {
				return
			}
			expect := vec.value
			if vec.want != nil {
				expect = vec.want
			}
			target := reflect.New(reflect.TypeOf(vec.value))
			if derr := c.Unmarshal(want.wire, target.Interface()); derr != nil {
				t.Fatalf("Unmarshal into %T: %v", vec.value, derr)
			}
			if back := target.Elem().Interface(); !reflect.DeepEqual(back, expect) {
				t.Errorf("round trip:\n got  %#v\n want %#v", back, expect)
			}
		})
	}
}
