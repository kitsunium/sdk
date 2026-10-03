package cbor_test

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/kitsunium/sdk/internal/service/data/codec/cbor"
)

// person matches keys by tag, by the json fallback and by Go name, which an
// empty tag leaves in place: it names nothing.
type person struct {
	Name  string `cbor:"name"`
	Age   int    `json:"age"`
	Email string `cbor:""`
	Skip  string `cbor:"-"`
	Kind  int    `cbor:"1,keyasint"`
	Neg   int    `cbor:"-3,keyasint"`
}

// Base is an exported struct, embedded by pointer in embeddedPtr.
type Base struct {
	// X is promoted.
	X int `cbor:"x"`
}

// embeddedPtr promotes the fields of an exported struct embedded by pointer,
// which the decoder allocates when a promoted field is present.
type embeddedPtr struct {
	*Base
	W int `cbor:"w"`
}

// twoCases has two fields whose keys differ only in case.
type twoCases struct {
	Lower string `cbor:"name"`
	Upper string `cbor:"NAME"`
}

// hidden is an unexported embedded struct reached through a pointer.
type hidden struct {
	H int `cbor:"h"`
}

// unexportedPtr embeds an unexported struct by pointer: its exported fields
// are promoted, but a nil pointer cannot be allocated through reflection.
type unexportedPtr struct {
	*hidden
	V int `cbor:"v"`
}

// badKeyAsInt is refused: its keyasint name is not an integer.
type badKeyAsInt struct {
	A int `cbor:"x,keyasint"`
}

// TestUnmarshal_structs pins how map keys find struct fields.
func TestUnmarshal_structs(t *testing.T) {
	t.Parallel()
	runTyped(t, []typedCase{
		{
			"by tag, json fallback and Go name", "a3646e616d656341646163616765182465456d61696c6161",
			ptrTo[person](),
			person{Name: "Ada", Age: 36, Email: "a"},
			"",
		},
		{"case-insensitive fallback", "a1644e414d4563416461", ptrTo[person](), person{Name: "Ada"}, ""},
		{"a folded key then the exact one: the first wins", "a2644e414d456131646e616d656132", ptrTo[person](), person{Name: "1"}, ""},
		{"an exact match wins over a folded one", "a1644e414d456131", ptrTo[twoCases](), twoCases{Upper: "1"}, ""},
		{"a folded match takes the first field", "a1644e614d656131", ptrTo[twoCases](), twoCases{Lower: "1"}, ""},
		{"integer keys", "a201072209", ptrTo[person](), person{Kind: 7, Neg: 9}, ""},
		{"unknown keys are skipped", "a2617a820102646e616d656141", ptrTo[person](), person{Name: "A"}, ""},
		{"an excluded field is never set", "a164536b69706178", ptrTo[person](), person{}, ""},
		{"a repeated key keeps its first value", "a2646e616d656131646e616d656132", ptrTo[person](), person{Name: "1"}, ""},
		{"a byte string key is refused", "a1446e616d656141", ptrTo[person](), person{}, "cannot name a field"},
		{"a tagged key is refused", "a1d864646e616d656141", ptrTo[person](), person{}, "cannot name a field"},
		{"an integer key beyond int64", "a21b8000000000000000010107", ptrTo[person](), person{Kind: 7}, "an unsigned integer into"},
		{
			"a mismatch is reported, the rest decoded", "a2636167656161646e616d656141", ptrTo[person](),
			person{Name: "A"},
			"the field cbor_test.person.age of type int",
		},
		{"null leaves a struct", "f6", ptrWith(person{Name: "keep"}), person{Name: "keep"}, ""},
		{"an array is refused", "820102", ptrTo[person](), nil, "an array or a map into"},
		{"embedded value", "a2617801617a02", ptrTo[outer](), outer{X: 1, Z: 2}, ""},
		{"embedded pointer allocated", "a2617805617701", ptrTo[embeddedPtr](), embeddedPtr{Base: &Base{X: 5}, W: 1}, ""},
		{"embedded pointer left nil", "a1617701", ptrTo[embeddedPtr](), embeddedPtr{W: 1}, ""},
		{"unexported nil embedded pointer", "a2616801617602", ptrTo[unexportedPtr](), unexportedPtr{V: 2}, "nil, unexported embedded pointer"},
		{"unparsable keyasint name", "a1617801", ptrTo[badKeyAsInt](), nil, "keyasint field whose name is not an integer"},
		{"toarray", "830161784109", ptrTo[toArray](), toArray{A: 1, B: "x", C: []byte{9}}, ""},
		{"toarray indefinite", "9f0161784109ff", ptrTo[toArray](), toArray{A: 1, B: "x", C: []byte{9}}, ""},
		{"toarray wrong count", "820102", ptrTo[toArray](), toArray{}, "an array or a map into"},
		{"toarray from a map", "a0", ptrTo[toArray](), nil, "an array or a map into"},
	})
}

// TestUnmarshal_wideStruct decodes into a struct of more fields than a bit
// set on the stack tracks, and with a name index rather than a scan.
func TestUnmarshal_wideStruct(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		fields int
	}
	tests := []tc{{"seventy fields", 70}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		fields := make([]reflect.StructField, tc.fields)
		values := make(map[string]int, tc.fields)
		for i := range fields {
			name := "F" + strconv.Itoa(i)
			fields[i] = reflect.StructField{Name: name, Type: reflect.TypeFor[int]()}
			values[name] = i
		}
		values["F69"] = 69
		data, err := cbor.New().Marshal(values)
		if err != nil {
			t.Fatalf("%s: Marshal: %v", tc.name, err)
		}
		target := reflect.New(reflect.StructOf(fields))
		if err := cbor.New().Unmarshal(data, target.Interface()); err != nil {
			t.Fatalf("%s: Unmarshal: %v", tc.name, err)
		}
		for i := range fields {
			if got := target.Elem().Field(i).Int(); got != int64(i) {
				t.Fatalf("%s: field %d = %d", tc.name, i, got)
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
