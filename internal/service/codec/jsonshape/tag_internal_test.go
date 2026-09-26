// Package jsonshape — the json tag grammar on the spellings where a naive
// strings.Cut on the first comma reads something else than the engine.
package jsonshape

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestParseTagReadsWhatTheEngineReads pins the tag reader field by field, and
// checks each name against the member json.Marshal writes for a one-field
// struct carrying the same tag.
func TestParseTagReadsWhatTheEngineReads(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		tag    reflect.StructTag
		want   tagOptions
		others bool
	}
	named := func(name string) tagOptions { return tagOptions{name: name, hasName: true} }
	tests := []tc{
		{"no tag", ``, tagOptions{name: "Field"}, false},
		{"an empty tag", `json:""`, tagOptions{name: "Field"}, false},
		{"options only", `json:",omitempty"`, tagOptions{name: "Field", omitEmpty: true}, true},
		{"a plain name", `json:"field"`, named("field"), true},
		{"a dash with a comma is a name", `json:"-,"`, named("-"), true},
		{"a quote keeps the identifier before it", `json:"a'b"`, named("a"), true},
		{"a name not starting an identifier before a quote is no name", `json:"1a'b,omitempty"`, tagOptions{name: "Field", omitEmpty: true}, true},
		{"a leading digit without a reserved character is a name", `json:"1a"`, named("1a"), true},
		{"a control character is part of the name", `json:"d\tx"`, named("d\tx"), true},
		{"a double comma", `json:"n,,omitempty"`, tagOptions{name: "n", hasName: true, omitEmpty: true}, true},
		{"a trailing comma", `json:"n,omitempty,"`, tagOptions{name: "n", hasName: true, omitEmpty: true}, true},
		{"a quoted format value holds a comma", `json:"n,format:'2006, 01',string"`, tagOptions{name: "n", hasName: true, quoted: true, formatted: true}, true},
		{"an escaped quote and a comma inside a quoted value", `json:"n,format:'x\\',y\"z',omitempty"`, tagOptions{name: "n", hasName: true, formatted: true, omitEmpty: true}, true},
		{"an unterminated format value", `json:"n,format:'x,omitempty"`, tagOptions{name: "n", hasName: true, omitEmpty: true}, true},
		{"an unknown case value sets nothing", `json:",case:bogus"`, tagOptions{name: "Field"}, false},
		{"a valid case value is an option", `json:",case:ignore"`, tagOptions{name: "Field", cased: true}, true},
		{"a misspelt option is ignored", `json:",omitEmpty"`, tagOptions{name: "Field"}, false},
		{"embed alone", `json:",embed"`, tagOptions{name: "Field", embed: true}, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		opts, ignored := parseTag(reflect.StructField{Name: "Field", Tag: c.tag})
		//: every tag here is read, none ignored.
		if ignored {
			t.Fatalf("%s: ignored", c.tag)
		}
		//: every option, the name first.
		if opts != c.want || opts.hasOthers() != c.others {
			t.Errorf("%s: read %+v (others %v), want %+v (others %v)", c.tag, opts, opts.hasOthers(), c.want, c.others)
		}
		//: the name the engine writes for the same tag.
		if !c.want.embed && !strings.Contains(string(c.tag), "format:") {
			//: compared with json.Marshal's own member.
			if got := engineName(t, c.tag); got != c.want.name {
				t.Errorf("%s: json.Marshal writes %q, the reader says %q", c.tag, got, c.want.name)
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

// engineName returns the one member name json.Marshal writes for a struct
// whose only field, Field, carries tag and holds a non-empty value.
func engineName(t *testing.T, tag reflect.StructTag) string {
	t.Helper()
	typ := reflect.StructOf([]reflect.StructField{{Name: "Field", Type: reflect.TypeFor[int](), Tag: tag}})
	value := reflect.New(typ).Elem()
	value.Field(0).SetInt(7)
	written, err := json.Marshal(value.Interface())
	//: a one-field struct is always encodable.
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var members map[string]json.RawMessage
	//: one object.
	if err := json.Unmarshal(written, &members); err != nil || len(members) != 1 {
		t.Fatalf("json.Marshal wrote %s", written)
	}
	//: its only member.
	for name := range members {
		return name
	}
	return ""
}

// TestParseTagIgnoresWhatIsNeverWritten pins the two ignored spellings.
func TestParseTagIgnoresWhatIsNeverWritten(t *testing.T) {
	t.Parallel()
	//: exactly "-".
	if _, ignored := parseTag(reflect.StructField{Name: "F", Tag: `json:"-"`}); !ignored {
		t.Error(`json:"-" is not ignored`)
	}
	//: unexported and not embedded, whatever its tag says.
	if _, ignored := parseTag(reflect.StructField{Name: "f", PkgPath: "p", Tag: `json:"f,embed"`}); !ignored {
		t.Error("an unexported field is not ignored")
	}
}
