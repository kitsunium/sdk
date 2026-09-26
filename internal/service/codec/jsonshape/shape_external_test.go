// Package jsonshape_test — what each kind of Go type is described as, and
// that the description of a type keeps access to the Go fields behind it.
package jsonshape_test

import (
	"encoding/json"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/kitsunium/sdk/internal/service/codec/jsonshape"
)

// Types whose own methods decide their encoding.
type (
	// ByValue writes its own JSON through a value receiver.
	ByValue struct{ A int }
	// ByPointer writes its own JSON through a pointer receiver.
	ByPointer struct{ A int }
	// Textual writes itself as text.
	Textual struct{ A int }
	// Appended appends itself as text.
	Appended struct{ A int }
	// Streamed gains json/v2's streaming method, MarshalJSONTo, from the
	// json.Number it embeds — the method set is what the shape reads.
	Streamed struct{ json.Number }
	// ByteText is a byte that writes itself as text, so a slice of it is an
	// array of strings rather than base64.
	ByteText uint8
	// PlainByte is a named byte: a slice of it is still base64.
	PlainByte uint8
	// Event embeds time.Time and so gains its MarshalJSON.
	Event struct {
		time.Time
		Name string
	}
)

// MarshalJSON writes a fixed value.
func (ByValue) MarshalJSON() ([]byte, error) { return []byte(`"by-value"`), nil }

// MarshalJSON writes a fixed value.
func (*ByPointer) MarshalJSON() ([]byte, error) { return []byte(`"by-pointer"`), nil }

// MarshalText writes a fixed value.
func (Textual) MarshalText() ([]byte, error) { return []byte("textual"), nil }

// AppendText appends a fixed value.
func (Appended) AppendText(b []byte) ([]byte, error) { return append(b, "appended"...), nil }

// MarshalText writes a fixed value.
func (ByteText) MarshalText() ([]byte, error) { return []byte("b"), nil }

// TestEachKindIsDescribed pins the kinds, formats and nullability of every
// category of Go type encoding/json lays out, and of the ones it hands over to
// the type itself.
func TestEachKindIsDescribed(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		typ      reflect.Type
		kind     jsonshape.Kind
		format   string
		nullable bool
	}
	tests := []tc{
		{"a bool", reflect.TypeFor[bool](), jsonshape.Boolean, "", false},
		{"an int", reflect.TypeFor[int](), jsonshape.Integer, "int", false},
		{"a uint8", reflect.TypeFor[uint8](), jsonshape.Integer, "uint8", false},
		{"a uintptr", reflect.TypeFor[uintptr](), jsonshape.Integer, "uintptr", false},
		{"a float32", reflect.TypeFor[float32](), jsonshape.Number, "float32", false},
		{"a string", reflect.TypeFor[string](), jsonshape.String, "", false},
		{"a pointer is its element, nullable", reflect.TypeFor[*int](), jsonshape.Integer, "int", true},
		{"two pointers are one", reflect.TypeFor[**string](), jsonshape.String, "", true},
		{"an interface", reflect.TypeFor[any](), jsonshape.Any, "", true},
		{"a nil type", nil, jsonshape.Any, "", true},
		{"bytes are base64", reflect.TypeFor[[]byte](), jsonshape.String, "base64", true},
		{"named bytes are base64", reflect.TypeFor[[]PlainByte](), jsonshape.String, "base64", true},
		{"bytes that write text are an array", reflect.TypeFor[[]ByteText](), jsonshape.Array, "", true},
		{"a byte array is an array of numbers", reflect.TypeFor[[4]byte](), jsonshape.Array, "", false},
		{"a slice", reflect.TypeFor[[]string](), jsonshape.Array, "", true},
		{"a map", reflect.TypeFor[map[string]int](), jsonshape.Map, "", true},
		{"a struct", reflect.TypeFor[struct{ A int }](), jsonshape.Object, "", false},
		{"time.Time", reflect.TypeFor[time.Time](), jsonshape.String, "date-time", false},
		{"time.Duration", reflect.TypeFor[time.Duration](), jsonshape.Integer, "duration-ns", false},
		{"json.Number", reflect.TypeFor[json.Number](), jsonshape.Number, "", false},
		{"json.RawMessage writes itself", reflect.TypeFor[json.RawMessage](), jsonshape.Any, "", false},
		{"a value-receiver Marshaler", reflect.TypeFor[ByValue](), jsonshape.Any, "", false},
		{"a pointer-receiver Marshaler", reflect.TypeFor[ByPointer](), jsonshape.Any, "", false},
		{"a json/v2 MarshalerTo", reflect.TypeFor[Streamed](), jsonshape.Any, "", false},
		{"a TextMarshaler", reflect.TypeFor[Textual](), jsonshape.String, "", false},
		{"a TextAppender", reflect.TypeFor[Appended](), jsonshape.String, "", false},
		{"netip.Addr writes text", reflect.TypeFor[netip.Addr](), jsonshape.String, "", false},
		{"a struct embedding time.Time gains its method", reflect.TypeFor[Event](), jsonshape.Any, "", false},
		{"a channel", reflect.TypeFor[chan int](), jsonshape.Unsupported, "", false},
		{"a function", reflect.TypeFor[func()](), jsonshape.Unsupported, "", false},
		{"a complex number", reflect.TypeFor[complex128](), jsonshape.Unsupported, "", false},
		{"an unsafe pointer", reflect.TypeFor[unsafe.Pointer](), jsonshape.Unsupported, "", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		shape := jsonshape.Of(c.typ)
		//: kind, refinement and nullability together.
		if shape.Kind != c.kind || shape.Format != c.format || shape.Nullable != c.nullable {
			t.Errorf("Of(%v) = %v/%q nullable=%v, want %v/%q nullable=%v",
				c.typ, shape.Kind, shape.Format, shape.Nullable, c.kind, c.format, c.nullable)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestOpaqueTypesAreWhatEncodingJSONWrites pins the opaque verdicts against
// json.Marshal itself: each value is written as the shape's kind says — a
// pointer receiver's method through a pointer, where encoding/json calls it.
func TestOpaqueTypesAreWhatEncodingJSONWrites(t *testing.T) {
	t.Parallel()
	for _, value := range []any{ByValue{}, &ByPointer{}, Textual{}, Appended{}, Streamed{Number: "1"}, netip.MustParseAddr("::1"), time.Unix(0, 0)} {
		written, err := json.Marshal(value)
		//: each is encodable.
		if err != nil {
			t.Fatalf("json.Marshal(%T): %v", value, err)
		}
		shape := jsonshape.Of(reflect.TypeOf(value))
		//: a string shape is written as a string; an opaque one as anything.
		if shape.Kind == jsonshape.String && written[0] != '"' {
			t.Errorf("%T is described as a string and written as %s", value, written)
		}
	}
}

// TestMapsAreDescribedByTheirKeys pins which keys encoding/json can write as
// member names, measured against json.Marshal: a map whose key it refuses is
// Unsupported, because the whole value is refused.
func TestMapsAreDescribedByTheirKeys(t *testing.T) {
	t.Parallel()
	one := "k"
	type tc struct {
		name  string
		value any
		kind  jsonshape.Kind
	}
	tests := []tc{
		{"string keys", map[string]int{"a": 1}, jsonshape.Map},
		{"integer keys", map[int]int{1: 1}, jsonshape.Map},
		{"unsigned keys", map[uintptr]int{1: 1}, jsonshape.Map},
		{"float keys", map[float64]int{1.5: 1}, jsonshape.Map},
		{"text keys", map[netip.Addr]int{netip.MustParseAddr("::1"): 1}, jsonshape.Map},
		{"time keys", map[time.Time]int{time.Unix(0, 0).UTC(): 1}, jsonshape.Map},
		{"pointer keys", map[*string]int{&one: 1}, jsonshape.Map},
		{"interface keys", map[any]int{"a": 1}, jsonshape.Map},
		{"bool keys", map[bool]int{true: 1}, jsonshape.Unsupported},
		{"struct keys", map[struct{ A int }]int{{1}: 1}, jsonshape.Unsupported},
		{"array keys", map[[2]byte]int{{1, 2}: 1}, jsonshape.Unsupported},
		{"pointer-receiver text keys", map[ByPointerText]int{{1}: 1}, jsonshape.Unsupported},
		{"JSON-writing keys", map[ByValue]int{{1}: 1}, jsonshape.Unsupported},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		shape := jsonshape.Of(reflect.TypeOf(c.value))
		//: the verdict.
		if shape.Kind != c.kind {
			t.Fatalf("Of(%T) = %v, want %v", c.value, shape.Kind, c.kind)
		}
		_, err := json.Marshal(c.value)
		//: and encoding/json agrees: a map it writes, or one it refuses.
		if (err == nil) != (c.kind == jsonshape.Map) {
			t.Errorf("json.Marshal(%T) error = %v, the shape says %v", c.value, err, c.kind)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// ByPointerText writes text through a pointer receiver: as a map key — never
// addressable — encoding/json does not call it.
type ByPointerText struct{ A int }

// MarshalText writes a fixed value.
func (*ByPointerText) MarshalText() ([]byte, error) { return []byte("k"), nil }

// Recursive types.
type (
	// Node reaches itself through a slice and a pointer.
	Node struct {
		Value    int
		Children []Node
		Parent   *Node
	}
	// Ping and Pong reach each other.
	Ping struct{ Pong *Pong }
	Pong struct{ Ping *Ping }
	// Forest is a slice of itself.
	Forest []Forest
	// Index is a map of itself.
	Index map[string]Index
	// Loop is a pointer to itself.
	Loop *Loop
)

// TestRecursionIsReferencedNotRepeated pins that a type reaching itself is
// described once, then referenced by name — through a slice, a pointer, a
// map, a pair of types, and a pointer type naming itself — and that a type
// used twice side by side is described twice, not referenced.
func TestRecursionIsReferencedNotRepeated(t *testing.T) {
	t.Parallel()
	node := jsonshape.For[Node]()
	name := "github.com/kitsunium/sdk/internal/service/codec/jsonshape_test.Node"
	//: the struct itself, named.
	if node.Name != name || node.Ref != "" {
		t.Fatalf("Node = %q ref %q, want the full description named %q", node.Name, node.Ref, name)
	}
	children := field(t, node, "Children").Shape.Items
	//: the element refers back.
	if children.Ref != name || children.Kind != jsonshape.Object || len(children.Fields) != 0 {
		t.Errorf("Children items = %+v, want a reference to Node", children)
	}
	parent := field(t, node, "Parent").Shape
	//: the pointer refers back, and may be null.
	if parent.Ref != name || !parent.Nullable {
		t.Errorf("Parent = %+v, want a nullable reference to Node", parent)
	}
	ping := jsonshape.For[Ping]()
	//: two types, each described once on the way down.
	if back := field(t, field(t, ping, "Pong").Shape, "Ping").Shape; !strings.HasSuffix(back.Ref, ".Ping") {
		t.Errorf("Ping.Pong.Ping = %+v, want a reference to Ping", back)
	}
	//: a slice and a map of themselves end one level down.
	if forest := jsonshape.For[Forest](); forest.Ref != "" || !strings.HasSuffix(forest.Items.Ref, ".Forest") {
		t.Errorf("Forest = %+v, want an array whose items refer back", forest)
	}
	//: a map of itself.
	if index := jsonshape.For[Index](); index.Ref != "" || !strings.HasSuffix(index.Values.Ref, ".Index") {
		t.Errorf("Index = %+v, want a map whose values refer back", index)
	}
	//: a pointer to itself is its own element, so its shape IS the reference:
	//: only null can ever be written, and the walk ends.
	if loop := jsonshape.For[Loop](); !loop.Nullable || !strings.HasSuffix(loop.Ref, ".Loop") {
		t.Errorf("Loop = %+v, want a nullable reference to itself", loop)
	}
	twice := jsonshape.For[struct{ A, B Base }]()
	//: a type used twice side by side is described twice.
	if len(field(t, twice, "A").Shape.Fields) != 2 || len(field(t, twice, "B").Shape.Fields) != 2 {
		t.Errorf("a repeated sibling type was referenced instead of described")
	}
}

// field returns the member named name, failing the test when there is none.
func field(t *testing.T, shape *jsonshape.ShapeValue, name string) jsonshape.FieldValue {
	t.Helper()
	//: by wire name.
	for _, candidate := range shape.Fields {
		//: found.
		if candidate.Name == name {
			return candidate
		}
	}
	t.Fatalf("no member %q in %+v", name, shape)
	return jsonshape.FieldValue{}
}

// Request is the shape of a framework's request type: path, query and header
// parameters in tags of the framework's own, the body in json.
type Request struct {
	ID      string `path:"id" json:"-"`
	Search  string `json:"Search" query:"q"`
	Trace   string `header:"X-Trace" json:"trace,omitempty"`
	Payload struct {
		Title string `json:"title" validate:"required,max=80"`
	} `json:"payload"`
	Common
}

// Common is promoted into Request.
type Common struct {
	Locale string `cookie:"locale" json:"locale,omitempty"`
}

// TestEachFieldKeepsItsGoField pins what a framework needs to adopt the
// shape: each member's struct tag, Go name and index path, so it can read its
// own tags beside encoding/json's — a promoted field included — and the
// validate tag carried as Rules.
func TestEachFieldKeepsItsGoField(t *testing.T) {
	t.Parallel()
	shape := jsonshape.For[Request]()
	typ := reflect.TypeFor[Request]()
	//: json:"-" is not a member; the rest are, in order.
	if got := shapeNames(shape); strings.Join(got, ",") != "Search,trace,payload,locale" {
		t.Fatalf("members = %q", got)
	}
	search := field(t, shape, "Search")
	//: a framework reads its own tag.
	if search.Tag.Get("query") != "q" || search.GoName != "Search" {
		t.Errorf("Search: tag %q, Go name %q", search.Tag, search.GoName)
	}
	locale := field(t, shape, "locale")
	//: a promoted field keeps its full path.
	if locale.Tag.Get("cookie") != "locale" || typ.FieldByIndex(locale.Index).Name != "Locale" || len(locale.Index) != 2 {
		t.Errorf("locale: tag %q, index %v", locale.Tag, locale.Index)
	}
	title := field(t, field(t, shape, "payload").Shape, "title")
	//: the validate tag, as written.
	if title.Rules != "required,max=80" {
		t.Errorf("title rules = %q", title.Rules)
	}
	//: and a field json leaves out is still the framework's to find.
	if _, found := reflect.TypeFor[Request]().FieldByName("ID"); !found {
		t.Error("the ID field vanished from the type")
	}
}

// TestAShapeEncodesAsReadableJSON pins the shape's own wire form: kinds by
// name, the Go-only fields left out.
func TestAShapeEncodesAsReadableJSON(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(jsonshape.For[struct {
		N int `json:"n,omitempty" query:"n"`
	}]())
	//: a shape is encodable.
	if err != nil {
		t.Fatalf("json.Marshal(shape): %v", err)
	}
	want := `{"kind":"object","fields":[{"name":"n","shape":{"kind":"integer","format":"int"},"optional":true}]}`
	//: the documented form.
	if string(encoded) != want {
		t.Errorf("shape = %s, want %s", encoded, want)
	}
}

// TestKindNames pins the spelling of every kind, and of one never minted.
func TestKindNames(t *testing.T) {
	t.Parallel()
	want := map[jsonshape.Kind]string{
		jsonshape.Any: "any", jsonshape.Object: "object", jsonshape.Array: "array", jsonshape.Map: "map",
		jsonshape.String: "string", jsonshape.Integer: "integer", jsonshape.Number: "number",
		jsonshape.Boolean: "boolean", jsonshape.Unsupported: "unsupported", jsonshape.Kind(200): "unknown",
	}
	//: each kind.
	for kind, name := range want {
		text, err := kind.MarshalText()
		//: String and MarshalText agree, and MarshalText never fails.
		if err != nil || kind.String() != name || string(text) != name {
			t.Errorf("Kind(%d) = %q / %q (%v), want %q", kind, kind.String(), text, err, name)
		}
	}
}

// TestAnUnsupportedFieldIsDescribedWhereItIs pins that Of never fails: a
// member encoding/json would refuse is described as Unsupported in place.
func TestAnUnsupportedFieldIsDescribedWhereItIs(t *testing.T) {
	t.Parallel()
	shape := jsonshape.For[struct {
		OK       int
		Callback func()
		Ignored  chan int `json:"-"`
	}]()
	//: the refusable member, described; the ignored one, absent.
	if field(t, shape, "Callback").Shape.Kind != jsonshape.Unsupported || len(shape.Fields) != 2 {
		t.Errorf("fields = %+v", shape.Fields)
	}
}
