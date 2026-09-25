package redact

import (
	"encoding/json"
	"reflect"
	"testing"
	"unicode/utf8"
)

// Test_escapedLength pins the one invariant the exact bound rests on: the
// length the copier budgets for a character is the length it writes, for
// every class of character — the short escapes, the \u escapes, the line
// separators, one- to four-byte UTF-8, and the replacement character invalid
// input becomes.
func Test_escapedLength(t *testing.T) {
	t.Parallel()
	characters := []rune{'a', '"', '\\', '\b', '\f', '\n', '\r', '\t', 0x00, 0x1f, ' ', 'é', '漢', '😀', '\u2028', '\u2029', utf8.RuneError, 0x7f}
	for _, character := range characters {
		written := appendEscaped(nil, character)
		if len(written) != escapedLength(character) {
			t.Errorf("%U: writes %d bytes, budgets %d", character, len(written), escapedLength(character))
		}
		quoted := appendQuoted(nil, string(character))
		var decoded string
		if err := json.Unmarshal(quoted, &decoded); err != nil || decoded != string(character) {
			t.Errorf("%U: %s does not read back (%v, %q)", character, quoted, err, decoded)
		}
	}
}

// Test_jsonName pins the encoding/json layout rules the plan follows: a
// field it does not write is not a member, "-," is a member named "-", an
// embedded struct without a name is flattened — through a pointer, and even
// when its own type is unexported. The fields are built by hand, because a
// struct literal with a `json:"-,"` tag is what linters are right to question
// everywhere except in the test of the one reader that must honour it.
func Test_jsonName(t *testing.T) {
	t.Parallel()
	type hidden struct{ Inner string }
	type Visible struct{ Inner string }
	stringType := reflect.TypeFor[string]()
	type tc struct {
		field        reflect.StructField
		wantName     string
		wantPromoted bool
		wantVisible  bool
	}
	tests := []tc{
		{reflect.StructField{Name: "hidden", PkgPath: "p", Type: reflect.TypeFor[hidden](), Anonymous: true}, "", true, true},
		{reflect.StructField{Name: "Visible", Type: reflect.TypeFor[*Visible](), Anonymous: true}, "", true, true},
		{reflect.StructField{Name: "Named", Type: reflect.TypeFor[Visible](), Tag: `json:"named"`}, "named", false, true},
		{reflect.StructField{Name: "Skipped", Type: stringType, Tag: `json:"-"`}, "", false, false},
		{reflect.StructField{Name: "Dash", Type: stringType, Tag: `json:"-,"`}, "-", false, true},
		{reflect.StructField{Name: "unexposed", PkgPath: "p", Type: stringType}, "", false, false},
		{reflect.StructField{Name: "Plain", Type: stringType}, "Plain", false, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		name, promoted, visible := jsonName(c.field)
		if name != c.wantName || promoted != c.wantPromoted || visible != c.wantVisible {
			t.Errorf("jsonName(%s) = %q, %v, %v; want %q, %v, %v",
				c.field.Name, name, promoted, visible, c.wantName, c.wantPromoted, c.wantVisible)
		}
	}
	for _, c := range tests {
		t.Run(c.field.Name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Redactor_planOf pins the cache and the recursion: a type is walked
// once per Redactor, and a type that refers to itself shares its own plan
// instead of walking forever.
func Test_Redactor_planOf(t *testing.T) {
	t.Parallel()
	type node struct {
		Secret string `json:"secret_value" redact:"secret"`
		Next   *node  `json:"next"`
	}
	r := NewRedactor(Config{})
	first := r.planOf(reflect.TypeFor[node]())
	if first == nil || first.member("next") != first {
		t.Fatalf("a recursive type's plan does not refer to itself: %+v", first)
	}
	if !first.member("secret_value").secret {
		t.Error("the declared field is not secret in the plan")
	}
	if r.planOf(reflect.TypeFor[node]()) != first {
		t.Error("a type seen before was walked again")
	}
	if r.planOf(reflect.TypeFor[int]()) != nil || r.planOf(nil) != nil {
		t.Error("a scalar or a nil type declares something")
	}
	if NewRedactor(Config{}).planOf(reflect.TypeFor[json.RawMessage]()) != nil {
		t.Error("a type that writes its own JSON is walked")
	}
}
