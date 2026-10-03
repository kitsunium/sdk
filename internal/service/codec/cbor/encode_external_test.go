package cbor_test

import (
	"encoding/hex"
	"errors"
	"math"
	"math/big"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/cbor"
)

// tagged exercises the struct tag matrix: cbor names, the json fallback,
// "-", omitempty, omitzero (in both tags), an empty tag — which names
// nothing, so the Go name is the key — and an unexported field.
type tagged struct {
	A int       `cbor:"a"`
	B string    `cbor:"b,omitempty"`
	C int       `json:"c"`
	D int       `cbor:"-"`
	E int       `cbor:""`
	F *int      `cbor:"f,omitempty"`
	G []int     `cbor:"g,omitempty"`
	H string    `json:"h,omitzero"`
	I time.Time `cbor:"i,omitempty"`
	J time.Time `cbor:"j,omitzero"`
	k int
}

// inner is embedded by outer and by pointer in pointerEmbed.
type inner struct {
	X int `cbor:"x"`
	Y int `cbor:"y,omitempty"`
}

// outer promotes inner's fields.
type outer struct {
	inner
	Z int `cbor:"z"`
}

// pointerEmbed promotes inner's fields through a pointer that may be nil.
type pointerEmbed struct {
	*inner
	W int `cbor:"w"`
}

// toArray is a struct encoded as an array of its fields.
type toArray struct {
	_ struct{} `cbor:",toarray"`
	A int
	B string
	C []byte
}

// keyAsInt keys two fields by integer, one of them negative.
type keyAsInt struct {
	A int    `cbor:"1,keyasint"`
	B string `cbor:"-2,keyasint"`
	C int    `cbor:"c"`
}

// myByte is a named byte, whose slices are still byte strings.
type myByte uint8

// myBytes is a named byte slice.
type myBytes []byte

// selfMarshaler writes its own CBOR.
type selfMarshaler struct {
	raw []byte
	err error
}

// MarshalCBOR returns the raw item, or the configured failure.
func (s selfMarshaler) MarshalCBOR() ([]byte, error) {
	return s.raw, s.err
}

// failingBinary is a BinaryMarshaler that fails.
type failingBinary struct{}

// MarshalBinary fails.
func (failingBinary) MarshalBinary() ([]byte, error) {
	return nil, errors.New("binary refused")
}

// selfPointer is a pointer type that points to itself.
type selfPointer *selfPointer

// cyclic is a struct that can point to itself.
type cyclic struct {
	Next *cyclic `cbor:"next"`
}

// mustHex decodes a fixture.
func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad fixture %q: %v", s, err)
	}
	return b
}

// TestMarshal_golden pins the bytes the encoder writes. Every vector is the
// encoding fxamacker/cbor produced with the options the codec used, so a
// reader of the codec's output sees no change.
func TestMarshal_golden(t *testing.T) {
	t.Parallel()
	one := 1
	ptr := &one
	type tc struct {
		name string
		in   any
		want string
	}
	tests := []tc{
		{"zero", 0, "00"},
		{"23", 23, "17"},
		{"24", 24, "1818"},
		{"256", 256, "190100"},
		{"65536", 65536, "1a00010000"},
		{"max uint32 + 1", maxUint32 + 1, "1b0000000100000000"},
		{"max int64", maxInt64, "1b7fffffffffffffff"},
		{"max uint64", maxUint64, "1bffffffffffffffff"},
		{"-1", -1, "20"},
		{"-25", -25, "3818"},
		{"min int64", minInt64, "3b7fffffffffffffff"},
		{"int8 -128", int8(-128), "387f"},
		{"float32", float32(1.5), "fa3fc00000"},
		{"float64", 1.5, "fb3ff8000000000000"},
		{"float64 zero", 0.0, "fb0000000000000000"},
		{"float64 negative zero", math.Copysign(0, -1), "fb8000000000000000"},
		{"NaN", math.NaN(), "f97e00"},
		{"float32 NaN", float32(math.NaN()), "f97e00"},
		{"+Inf", math.Inf(1), "f97c00"},
		{"-Inf", math.Inf(-1), "f9fc00"},
		{"float32 +Inf", float32(math.Inf(1)), "f97c00"},
		{"large float64", 1e300, "fb7e37e43c8800759c"},
		{"true", true, "f5"},
		{"false", false, "f4"},
		{"nil", nil, "f6"},
		{"string", "hello", "6568656c6c6f"},
		{"nil bytes", []byte(nil), "f6"},
		{"empty bytes", []byte{}, "40"},
		{"bytes", []byte{1, 2, 3}, "43010203"},
		{"byte array", [4]byte{1, 2, 3, 4}, "4401020304"},
		{"named byte elements", []myByte{1, 2}, "420102"},
		{"named byte slice", myBytes{1, 2}, "420102"},
		{"empty int array", [0]int{}, "80"},
		{"nil slice", []int(nil), "f6"},
		{"empty slice", []int{}, "80"},
		{"slice", []int{1, 2, 3}, "83010203"},
		{"float32 slice", []float32{1.5}, "81fa3fc00000"},
		{"untyped slice", []any{1, "a", nil, true, 1.5}, "85016161f6f5fb3ff8000000000000"},
		{"nil map", map[string]int(nil), "f6"},
		{"empty map", map[string]int{}, "a0"},
		{"one-pair map", map[string]int{"a": 1}, "a1616101"},
		{"int-keyed map", map[int]string{1: "x"}, "a1016178"},
		{"untyped map", map[string]any{"a": 1}, "a1616101"},
		{"zero struct", tagged{}, "a46161006163006145006169f6"},
		{
			"full struct",
			tagged{A: 1, B: "b", C: 3, D: 4, E: 5, F: &one, G: []int{1}, H: "h", I: time.Unix(10, 0), J: time.Unix(11, 0), k: 9},
			"a961610161626162616303614505616601616781016168616861690a616a0b",
		},
		{"embedded struct", outer{X: 1, Z: 2}, "a2617801617a02"},
		{"nil embedded pointer", pointerEmbed{W: 1}, "a1617701"},
		{"embedded pointer", pointerEmbed{inner: &inner{X: 5}, W: 1}, "a2617805617701"},
		{"toarray", toArray{A: 1, B: "x", C: []byte{9}}, "830161784109"},
		{"keyasint", keyAsInt{A: 1, B: "x", C: 3}, "a30101216178616303"},
		{"empty struct", struct{}{}, "a0"},
		{"interface field", struct{ V any }{V: 3}, "a1615603"},
		{"zero time", time.Time{}, "f6"},
		{"time", time.Unix(1700000000, 0), "1a6553f100"},
		{"time drops the fraction", time.Unix(1700000000, 999999999), "1a6553f100"},
		{"time before 1970", time.Unix(-1, 500000000), "20"},
		{"pointer to zero time", &time.Time{}, "f6"},
		{"small big.Int", big.NewInt(42), "182a"},
		{"negative big.Int", big.NewInt(-42), "3829"},
		{"big.Int value", *big.NewInt(7), "07"},
		{"2^64 bignum", new(big.Int).Lsh(big.NewInt(1), 64), "c249010000000000000000"},
		{"-2^64-1 bignum", new(big.Int).Not(new(big.Int).Lsh(big.NewInt(1), 64)), "c349010000000000000000"},
		{"-2^64 fits a head", new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 64)), "3bffffffffffffffff"},
		{"BinaryMarshaler (url)", mustURL(t, "https://a.b/c"), "4d68747470733a2f2f612e622f63"},
		{"net.IP is a byte slice", net.ParseIP("127.0.0.1"), "5000000000000000000000ffff7f000001"},
		{"pointer to pointer", &ptr, "01"},
		{"nil pointer", (*int)(nil), "f6"},
		{"interface holding a nil pointer", any((*int)(nil)), "f6"},
		{"MarshalCBOR", selfMarshaler{raw: []byte{0xc1, 0x01}}, "c101"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, err := cbor.New().Marshal(tc.in)
		if err != nil {
			t.Fatalf("%s: Marshal err = %v (%s)", tc.name, err, errs.PrivateOf(err))
		}
		if hex.EncodeToString(got) != tc.want {
			t.Errorf("%s: Marshal = %x, want %s", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// mustURL parses a URL fixture.
func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("bad URL fixture: %v", err)
	}
	return u
}

// TestMarshal_sortedMaps pins the deterministic order: pairs sorted by the
// bytes of their encoded keys (RFC 8949 §4.2.1), whatever Go's map order.
func TestMarshal_sortedMaps(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   any
		want string
	}
	tests := []tc{
		{
			"text keys shortest first",
			map[string]int{"bb": 2, "a": 1, "c": 3, "aa": 4},
			"a46161016163036261610462626202",
		},
		{"string values", map[string]string{"z": "1", "y": "2"}, "a261796132617a6131"},
		{
			"untyped keys of every kind",
			map[any]any{"a": 1, 10: 2, -1: 3, false: 4},
			"a40a022003616101f404",
		},
		{
			"integer keys",
			map[int]bool{256: true, 1: false, -2: true, 24: false},
			"a401f41818f4190100f521f5",
		},
		{
			"untyped document",
			map[string]any{"name": "ada", "id": uint64(7), "tags": []any{"x"}},
			"a362696407646e616d65636164616474616773816178",
		},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		for range 8 {
			got, err := cbor.New().Marshal(tc.in)
			if err != nil {
				t.Fatalf("%s: Marshal err = %v", tc.name, err)
			}
			if hex.EncodeToString(got) != tc.want {
				t.Fatalf("%s: Marshal = %x, want %s", tc.name, got, tc.want)
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

// TestMarshal_refusals pins every value the encoder refuses to write: a type
// CBOR cannot carry, invalid UTF-8, two keys that encode alike, nesting the
// decoder would refuse, cycles, and a failing or lying marshaler.
func TestMarshal_refusals(t *testing.T) {
	t.Parallel()
	loop := &cyclic{}
	loop.Next = loop
	var self any
	self = &self
	var selfPtr selfPointer
	selfPtr = &selfPtr
	type tc struct {
		name   string
		in     any
		detail string
	}
	tests := []tc{
		{"channel", make(chan int), "cannot carry: chan int"},
		{"func", func() {}, "cannot carry"},
		{"complex", complex(1, 2), "cannot carry"},
		{"uintptr", uintptr(1), "cannot carry"},
		{"map of channels", map[string]chan int{"a": nil}, "cannot carry"},
		{"map keyed by channels", map[chan int]int{}, "cannot carry"},
		{"slice of channels", []chan int{nil}, "cannot carry"},
		{"pointer to a channel", new(chan int), "cannot carry"},
		{"struct with a func field", struct{ F func() }{}, "cannot carry"},
		{"invalid UTF-8 string", "\xff\xfe", "not valid UTF-8"},
		{"invalid UTF-8 key", map[string]int{"\xff": 1}, "not valid UTF-8"},
		{"equal encoded keys", map[any]any{1: "a", uint64(1): "b"}, "encode to the same CBOR key"},
		{"two NaN keys", map[float64]int{math.NaN(): 1, math.NaN(): 2}, "encode to the same CBOR key"},
		{"33 nested slices", nestedSlices(33), "nested more than 32"},
		{"struct cycle", loop, "nested more than 32"},
		{"interface cycle", self, "more than 32 pointers and interfaces"},
		{"self-referential pointer type", selfPtr, "self-referential pointer type"},
		{"MarshalCBOR failure", selfMarshaler{err: errors.New("no")}, "MarshalCBOR failed"},
		{"MarshalCBOR not one item", selfMarshaler{raw: []byte{0x01, 0x02}}, "other than one valid CBOR data item"},
		{"MarshalCBOR empty", selfMarshaler{raw: nil}, "other than one valid CBOR data item"},
		{"BinaryMarshaler failure", failingBinary{}, "MarshalBinary failed"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := cbor.New().Marshal(tc.in)
		if !errs.HasReason(err, "MARSHAL_FAILED") {
			t.Fatalf("%s: err = %v, want MARSHAL_FAILED", tc.name, err)
		}
		if !strings.Contains(errs.PrivateOf(err), tc.detail) {
			t.Errorf("%s: Private = %q, want it to mention %q", tc.name, errs.PrivateOf(err), tc.detail)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// nestedSlices builds depth levels of []any around an integer.
func nestedSlices(depth int) any {
	var v any = 1
	for range depth {
		v = []any{v}
	}
	return v
}

// TestMarshal_depthBound writes exactly as deep as the decoder reads: 32
// levels encode and decode, 33 do not encode.
func TestMarshal_depthBound(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		depth int
		ok    bool
	}
	tests := []tc{{"32 levels", 32, true}, {"33 levels", 33, false}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		data, err := cbor.New().Marshal(nestedSlices(tc.depth))
		if (err == nil) != tc.ok {
			t.Fatalf("%s: Marshal err = %v, want ok %v", tc.name, err, tc.ok)
		}
		if tc.ok {
			var back any
			if uerr := cbor.New().Unmarshal(data, &back); uerr != nil {
				t.Errorf("%s: the decoder refused what the encoder wrote: %v", tc.name, uerr)
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

// wideIntMap maps 0..n-1 to themselves, wide enough for the general sort.
func wideIntMap(n int) map[int]int {
	m := make(map[int]int, n)
	for i := range n {
		m[i] = i
	}
	return m
}

// wideIntMapHex is wideIntMap(n)'s encoding, n < 24: its keys in order.
func wideIntMapHex(n int) string {
	var b strings.Builder
	b.WriteString(hex.EncodeToString([]byte{0xa0 | byte(n)}))
	for i := range n {
		b.WriteString(hex.EncodeToString([]byte{byte(i), byte(i)}))
	}
	return b.String()
}
