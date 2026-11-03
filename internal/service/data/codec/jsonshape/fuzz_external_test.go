package jsonshape_test

import (
	stdjson "encoding/json"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/kitsunium/sdk/internal/service/data/codec/jsonshape"
)

// FuzzEmbedded is a named struct the generated types may embed, so its
// fields are promoted — or shadowed — as encoding/json decides.
type FuzzEmbedded struct {
	A int    `json:"a"`
	B string `json:"x,omitempty"`
}

// The tags and types a generated field picks from: names that collide on
// purpose, every option encoding/json reads, and the kinds jsonshape tells
// apart.
var (
	fuzzTags  = []string{``, `json:"x"`, `json:"y,omitempty"`, `json:"-"`, `json:"-,"`, `json:"x,string"`, `json:",omitempty"`, `json:"a"`, `json:"z,omitzero"`}
	fuzzTypes = []reflect.Type{
		reflect.TypeFor[int](), reflect.TypeFor[string](), reflect.TypeFor[*int](), reflect.TypeFor[[]int](),
		reflect.TypeFor[map[string]int](), reflect.TypeFor[bool](), reflect.TypeFor[FuzzEmbedded](),
	}
)

// generated builds a struct type from spec, two bytes a field: the tag, then
// the type — a FuzzEmbedded with the high bit set is embedded rather than named.
func generated(spec []byte) reflect.Type {
	var fields []reflect.StructField
	embedded := false
	//: at most eight fields.
	for i := 0; i+1 < len(spec) && len(fields) < 8; i += 2 {
		typ := fuzzTypes[int(spec[i+1]&0x7f)%len(fuzzTypes)]
		field := reflect.StructField{Name: "F" + strconv.Itoa(len(fields)), Type: typ, Tag: reflect.StructTag(fuzzTags[int(spec[i])%len(fuzzTags)])}
		//: one embedded FuzzEmbedded at most: its name is its type's.
		if typ == reflect.TypeFor[FuzzEmbedded]() && spec[i+1]&0x80 != 0 && !embedded {
			field.Name, field.Anonymous, embedded = "FuzzEmbedded", true, true
		}
		fields = append(fields, field)
	}
	return reflect.StructOf(fields)
}

// fill sets every field of the struct v to a value no omitempty or omitzero
// drops.
func fill(v reflect.Value) {
	for i := range v.NumField() {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.Int:
			f.SetInt(1)
		case reflect.String:
			f.SetString("s")
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Pointer:
			f.Set(reflect.New(f.Type().Elem()))
		case reflect.Slice:
			f.Set(reflect.ValueOf([]int{1}))
		case reflect.Map:
			f.Set(reflect.ValueOf(map[string]int{"k": 1}))
		case reflect.Struct:
			fill(f)
		default:
		}
	}
}

// writtenKeys returns the sorted member names encoding/json writes for v.
func writtenKeys(t *testing.T, v reflect.Value) []string {
	t.Helper()
	raw, err := stdjson.Marshal(v.Interface())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var members map[string]stdjson.RawMessage
	if err := stdjson.Unmarshal(raw, &members); err != nil {
		t.Fatalf("Unmarshal(%s): %v", raw, err)
	}
	keys := make([]string, 0, len(members))
	for k := range members {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// FuzzOfAgreesWithMarshal is #897's jsonshape target: for a struct type built
// from the input — colliding names, every tag option, an embedded struct —
// the members Of reports are exactly the keys encoding/json writes for a value
// with every field set, and the keys it writes for the zero value are among
// them, every member it leaves out reported optional.
func FuzzOfAgreesWithMarshal(f *testing.F) {
	for _, seed := range [][]byte{{0, 0, 1, 1}, {1, 0, 1, 1}, {7, 0, 0, 0x86}, {2, 4, 6, 3, 8, 5}, {3, 0, 4, 0}, {5, 0, 5, 1}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, spec []byte) {
		typ := generated(spec)
		shape := jsonshape.Of(typ)
		var names []string
		optional := map[string]bool{}
		for _, field := range shape.Fields {
			names = append(names, field.Name)
			optional[field.Name] = field.Optional
		}
		slices.Sort(names)
		full := reflect.New(typ).Elem()
		fill(full)
		//: every member, and only those.
		if got := writtenKeys(t, full); !slices.Equal(got, names) {
			t.Fatalf("%v: encoding/json writes %q, Of reports %q", typ, got, names)
		}
		zero := writtenKeys(t, reflect.New(typ).Elem())
		//: the zero value writes members Of knows; the others are optional.
		for _, name := range names {
			if !slices.Contains(zero, name) && !optional[name] {
				t.Fatalf("%v: the zero value leaves %q out, which Of does not call optional", typ, name)
			}
		}
	})
}
