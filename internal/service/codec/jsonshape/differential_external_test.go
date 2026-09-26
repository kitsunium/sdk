// Package jsonshape_test — encoding/json is the oracle. For each struct below,
// the members a fully populated value marshals to, in order, must be the
// shape's fields; the members its zero value marshals to must be exactly the
// fields not marked Optional; each member's JSON kind must be its shape's;
// and the value at each field's Index must be the one written under its name.
package jsonshape_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/codec/jsonshape"
)

// Embedded types for the promotion rules.
type (
	// Base is embedded by several cases.
	Base struct {
		ID   int
		Name string
	}
	// Left and Right both declare Shared, untagged.
	Left struct {
		Shared   int
		OnlyLeft int
	}
	Right struct {
		Shared    int
		OnlyRight int
	}
	// TaggedLeft and TaggedRight declare Shared under a tag.
	TaggedLeft struct {
		Shared int `json:"Shared"`
	}
	TaggedRight struct {
		Shared int `json:"Shared"`
	}
	// Inner declares Shared one level further down.
	Inner struct{ Shared int }
	// Deep embeds Inner, so Shared is at depth 2 from its embedder.
	Deep struct{ Inner }
	// Corner is embedded twice through two paths.
	Corner struct{ X int }
	PathA  struct {
		Corner
		A int
	}
	PathB struct {
		Corner
		B int
	}
	// hidden is an unexported struct whose exported fields are promoted.
	hidden struct {
		Visible int
		secret  int
	}
	// Label is an embedded non-struct: a member named by its type.
	Label string
	// Payload is a named field embedded through json/v2's embed option.
	Payload struct {
		P1 int    `json:"P1"`
		P2 string `json:"P2"`
	}
)

// The cases declared in source.
type (
	// TwoDepths: the shallow Name wins over the promoted one.
	TwoDepths struct {
		Base
		Name string
	}
	// TwiceAtOneDepth: Shared ties untagged at depth 1 — neither is written.
	TwiceAtOneDepth struct {
		Left
		Right
	}
	// TaggedWins: a tagged Shared beats an untagged one at the same depth.
	TaggedWins struct {
		TaggedLeft
		Right
	}
	// TieHidesDeeper: the depth-1 tie annihilates Shared, and Deep's depth-2
	// Shared is not written either.
	TieHidesDeeper struct {
		Left
		Right
		Deep
	}
	// Diamond: Corner.X reached twice at depth 2 — neither is written.
	Diamond struct {
		PathA
		PathB
	}
	// ThroughPointer: Base's fields are promoted through a pointer, so a nil
	// one drops them.
	ThroughPointer struct {
		*Base
		Extra int
	}
	// WithHidden: an unexported embedded struct lends its exported fields.
	WithHidden struct {
		hidden
		Own int
	}
	// WithLabel: an embedded string type is a member named Label.
	WithLabel struct {
		Label
		Own int
	}
	// TaggedEmbedding: a named embedded struct is a member, not promoted.
	TaggedEmbedding struct {
		Base `json:"base"`
		Own  int `json:"Own"`
	}
	// Omissions: omitempty and omitzero across kinds.
	Omissions struct {
		StructZero  struct{ A int }   `json:"structZero,omitzero"`
		Array       [2]int            `json:"array,omitempty"`
		EmptyArray  [0]int            `json:"emptyArray,omitempty"`
		Pointer     *int              `json:"pointer,omitempty"`
		PlainNil    *int              `json:"plainNil"`
		TimeZero    time.Time         `json:"timeZero,omitzero"`
		Map         map[string]int    `json:"map,omitempty"`
		Slice       []string          `json:"slice,omitempty"`
		Bool        bool              `json:"bool,omitempty"`
		Interface   any               `json:"interface,omitempty"`
		Bytes       []byte            `json:"bytes,omitempty"`
		Number      json.Number       `json:"number,omitempty"`
		Uintptr     uintptr           `json:"uintptr,omitempty"`
		Float       float32           `json:"float,omitempty"`
		NestedEmpty map[string][]byte `json:"nestedEmpty,omitzero"`
	}
	// Quoting: ",string" on the kinds it applies to.
	Quoting struct {
		Int      int           `json:"int,string"`
		Bool     bool          `json:"bool,string"`
		Float    float64       `json:"float,string"`
		Str      string        `json:"str,string"`
		Pointer  *int          `json:"pointer,string"`
		Number   json.Number   `json:"number,string"`
		Duration time.Duration `json:"duration,string"`
	}
	// WithCollector: a map embedded with json/v2's embed option takes the
	// members no field names.
	WithCollector struct {
		Known int            `json:"Known"`
		Extra map[string]int `json:",embed"`
	}
)

// runtimeField is one field of a struct built at run time.
type runtimeField struct {
	name  string
	value any
	tag   reflect.StructTag
	embed bool
}

// built returns a value of a struct type built at run time from fields, each
// set to its value.
//
// The cases below declare what go vet and staticcheck refuse in source —
// rightly, since each is a mistake or a surprise in ordinary code: two fields
// tagged "Shared" at one depth, a member named "-", a name holding a quote, the
// string option on a slice, omitempty on a struct. Those spellings are exactly
// what the resolution must get right, and encoding/json resolves a type built
// with reflect.StructOf exactly as it resolves a declared one.
func built(fields ...runtimeField) any {
	declared := make([]reflect.StructField, 0, len(fields))
	//: one field per entry, typed by its value.
	for _, field := range fields {
		declared = append(declared, reflect.StructField{
			Name: field.name, Type: reflect.TypeOf(field.value), Tag: field.tag, Anonymous: field.embed,
		})
	}
	value := reflect.New(reflect.StructOf(declared)).Elem()
	//: every field set, so every member is written.
	for index, field := range fields {
		value.Field(index).Set(reflect.ValueOf(field.value))
	}
	return value.Interface()
}

// differentialCase is one struct, populated so every field is written.
type differentialCase struct {
	name string
	full any
}

// differentialCases are the structs encoding/json is asked about.
func differentialCases() []differentialCase {
	one := 1
	return []differentialCase{
		{"a shallow field hides a promoted one", TwoDepths{Base: Base{ID: 1, Name: "deep"}, Name: "shallow"}},
		{"an untagged tie writes neither", TwiceAtOneDepth{Left{Shared: 1, OnlyLeft: 2}, Right{Shared: 3, OnlyRight: 4}}},
		{"a tagged field wins a tie", TaggedWins{TaggedLeft{Shared: 5}, Right{Shared: 6, OnlyRight: 7}}},
		{"a tagged tie writes neither", built(
			runtimeField{name: "TaggedLeft", value: TaggedLeft{Shared: 8}, embed: true},
			runtimeField{name: "TaggedRight", value: TaggedRight{Shared: 9}, embed: true},
		)},
		{"a tie hides every deeper field", TieHidesDeeper{Left{Shared: 1, OnlyLeft: 2}, Right{Shared: 3, OnlyRight: 4}, Deep{Inner{Shared: 5}}}},
		{"a type reached twice cancels out", Diamond{PathA{Corner{X: 1}, 2}, PathB{Corner{X: 3}, 4}}},
		{"promotion through a pointer", ThroughPointer{Base: &Base{ID: 1, Name: "n"}, Extra: 2}},
		{"an unexported embedded struct", WithHidden{hidden{Visible: 1, secret: 2}, 3}},
		{"an embedded non-struct", WithLabel{Label: "l", Own: 1}},
		{"a named embedded struct", TaggedEmbedding{ID: 1, Name: "n", Own: 2}},
		{"dashes", built(
			runtimeField{name: "Skip", value: 1, tag: `json:"-"`},
			runtimeField{name: "Dash", value: 2, tag: `json:"-,"`},
			runtimeField{name: "Keep", value: 3},
		)},
		{"omissions", Omissions{
			StructZero: struct{ A int }{2}, Array: [2]int{1, 2}, Pointer: &one, PlainNil: &one,
			TimeZero: time.Unix(2, 0).UTC(), Map: map[string]int{"k": 1}, Slice: []string{"s"}, Bool: true,
			Interface: "i", Bytes: []byte{1}, Number: "7", Uintptr: 9, Float: 1.5,
			NestedEmpty: map[string][]byte{"k": {1}},
		}},
		{"omitempty never omits a struct or a full array", built(
			runtimeField{name: "Struct", value: struct{ A int }{1}, tag: `json:"struct,omitempty"`},
			runtimeField{name: "Time", value: time.Unix(1, 0).UTC(), tag: `json:"time,omitempty"`},
			runtimeField{name: "Array", value: [2]int{1, 2}, tag: `json:"array,omitempty"`},
		)},
		{"quoting", Quoting{Int: 1, Bool: true, Float: 1.5, Str: "s", Pointer: &one, Number: "12", Duration: time.Second}},
		{"the string option on a slice is ignored", built(
			runtimeField{name: "Slice", value: []int{1}, tag: `json:"slice,string"`},
		)},
		{"names and embed as the engine reads them", built(
			runtimeField{name: "Quote", value: 1, tag: `json:"a'b"`},
			runtimeField{name: "Tab", value: 2, tag: `json:"d\tx"`},
			runtimeField{name: "Accented", value: 3, tag: `json:"é"`},
			runtimeField{name: "Payload", value: Payload{P1: 4, P2: "p"}, tag: `json:",embed"`},
			runtimeField{name: "Last", value: 5},
		)},
		{"an embedded collector", WithCollector{Known: 1, Extra: map[string]int{"zeta": 2}}},
		{"an embedding's own options are dropped", built(
			runtimeField{name: "Base", value: Base{ID: 1, Name: "n"}, tag: `json:",omitempty"`, embed: true},
			runtimeField{name: "Own", value: 2},
		)},
	}
}

// TestShapesMatchWhatEncodingJSONWrites runs every case against json.Marshal.
func TestShapesMatchWhatEncodingJSONWrites(t *testing.T) {
	t.Parallel()
	runCase := func(t *testing.T, c differentialCase) {
		t.Helper()
		typ := reflect.TypeOf(c.full)
		shape := jsonshape.Of(typ)
		//: every case is an object.
		if shape.Kind != jsonshape.Object {
			t.Fatalf("Kind = %v, want object", shape.Kind)
		}
		full, err := json.Marshal(c.full)
		//: every case is encodable.
		if err != nil {
			t.Fatalf("json.Marshal(full): %v", err)
		}
		names, values := objectMembers(t, full)
		written := writtenFields(shape, values)
		//: the fields written, in encoding/json's order; a collector's members follow.
		if len(names) < len(written) || !slices.Equal(names[:len(written)], written) {
			t.Fatalf("json.Marshal wrote %q, the shape has %q", names, shapeNames(shape))
		}
		//: members past the fields exist only where a collector takes them.
		if extra := names[len(written):]; len(extra) > 0 && shape.Values == nil {
			t.Fatalf("json.Marshal wrote extra members %q the shape does not describe", extra)
		}
		value := reflect.ValueOf(c.full)
		//: each field against what was written under its name.
		for _, field := range shape.Fields {
			raw, wasWritten := values[field.Name]
			//: only an Optional field may be missing from a populated value — a
			//: zero-length array under omitempty is never written at all.
			if !wasWritten {
				if !field.Optional {
					t.Errorf("member %q is not Optional and was not written", field.Name)
				}
				continue
			}
			checkMember(t, field, raw, value)
		}
		zero, err := json.Marshal(reflect.Zero(typ).Interface())
		//: the zero value is encodable too.
		if err != nil {
			t.Fatalf("json.Marshal(zero): %v", err)
		}
		present, _ := objectMembers(t, zero)
		//: exactly the fields an encoded object cannot lack.
		if want := requiredNames(shape); !slices.Equal(present, want) {
			t.Errorf("the zero value wrote %q; the fields not Optional are %q", present, want)
		}
	}
	for _, c := range differentialCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// checkMember compares one field of the shape with the member json.Marshal
// wrote under its name.
func checkMember(t *testing.T, field jsonshape.FieldValue, raw json.RawMessage, value reflect.Value) {
	t.Helper()
	//: the member's JSON kind is the shape's — a quoted one is a string.
	if !kindMatches(field, raw) {
		t.Errorf("member %q is %s, the shape says %v (quoted %v)", field.Name, raw, field.Shape.Kind, field.Quoted)
	}
	goField := value.Type().FieldByIndex(field.Index)
	//: the Go field behind the member is reachable through the index.
	if goField.Name != field.GoName || goField.Tag != field.Tag {
		t.Errorf("member %q: Index %v reaches %s, want %s", field.Name, field.Index, goField.Name, field.GoName)
	}
	behind, err := value.FieldByIndexErr(field.Index)
	//: a populated case never has a nil pointer on the way.
	if err != nil {
		t.Fatalf("member %q: %v", field.Name, err)
	}
	//: a field read through an unexported embedding cannot be marshaled on
	//: its own; its kind check above stands.
	if !behind.CanInterface() || field.Quoted {
		return
	}
	alone, err := json.Marshal(behind.Interface())
	//: every field here is encodable alone.
	if err != nil {
		t.Fatalf("member %q: %v", field.Name, err)
	}
	//: the value written under the name is the value at the index — which
	//: is what shows the right field won a tie.
	if !bytes.Equal(alone, raw) {
		t.Errorf("member %q: json.Marshal wrote %s, the field at %v holds %s", field.Name, raw, field.Index, alone)
	}
}

// objectMembers decodes the top-level object of document: its member names in
// order, and each member's raw value.
func objectMembers(t *testing.T, document []byte) (names []string, values map[string]json.RawMessage) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	token, err := decoder.Token()
	//: an object.
	if err != nil || token != json.Delim('{') {
		t.Fatalf("%s is not an object: %v", document, err)
	}
	values = map[string]json.RawMessage{}
	//: member by member.
	for decoder.More() {
		key, keyErr := decoder.Token()
		var raw json.RawMessage
		//: a name, then a value.
		if keyErr != nil || decoder.Decode(&raw) != nil {
			t.Fatalf("%s: unreadable member", document)
		}
		name, _ := key.(string)
		names = append(names, name)
		values[name] = raw
	}
	return names, values
}

// shapeNames lists an object shape's member names.
func shapeNames(shape *jsonshape.ShapeValue) []string {
	names := make([]string, 0, len(shape.Fields))
	//: in order.
	for _, field := range shape.Fields {
		names = append(names, field.Name)
	}
	return names
}

// writtenFields lists the shape's member names that json.Marshal wrote, in the
// shape's order.
func writtenFields(shape *jsonshape.ShapeValue, values map[string]json.RawMessage) []string {
	var names []string
	//: in order.
	for _, field := range shape.Fields {
		//: present in the document.
		if _, ok := values[field.Name]; ok {
			names = append(names, field.Name)
		}
	}
	return names
}

// requiredNames lists the members an encoded object cannot lack.
func requiredNames(shape *jsonshape.ShapeValue) []string {
	var names []string
	//: in order.
	for _, field := range shape.Fields {
		//: not Optional.
		if !field.Optional {
			names = append(names, field.Name)
		}
	}
	return names
}

// kindMatches reports whether raw is a JSON value of the field's kind.
func kindMatches(field jsonshape.FieldValue, raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	//: nothing to judge.
	if len(trimmed) == 0 {
		return false
	}
	first := trimmed[0]
	//: a quoted value is a string, whatever it holds.
	if field.Quoted {
		return first == '"'
	}
	switch field.Shape.Kind {
	//: anything.
	case jsonshape.Any:
		return true
	//: an object.
	case jsonshape.Object, jsonshape.Map:
		return first == '{'
	//: an array.
	case jsonshape.Array:
		return first == '['
	//: a string.
	case jsonshape.String:
		return first == '"'
	//: a number.
	case jsonshape.Integer, jsonshape.Number:
		return first == '-' || (first >= '0' && first <= '9')
	//: a boolean.
	case jsonshape.Boolean:
		return first == 't' || first == 'f'
	//: never written.
	default:
		return false
	}
}
