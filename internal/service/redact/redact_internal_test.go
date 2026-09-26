package redact

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// The four layouts a field can have, as Test_jsonName states its
// expectations: one value rather than three booleans.
const (
	skipped   layout = iota // never written
	flattened               // an embedded struct whose fields are promoted
	untagged                // written under its Go name
	tagged                  // written under the name its json tag gives
)

// layout is how encoding/json treats one struct field.
type layout uint8

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
// when its own type is unexported — an unexported embedded struct WITH a name
// is a member of its own, an embedded non-struct is a member when exported and
// nothing otherwise, and a name taken from the tag says so. The fields are
// built by hand, because a struct literal with a `json:"-,"` tag is what
// linters are right to question everywhere except in the test of the one
// reader that must honour it.
func Test_jsonName(t *testing.T) {
	t.Parallel()
	type hidden struct{ Inner string }
	type Visible struct{ Inner string }
	type word string
	type Word string
	stringType := reflect.TypeFor[string]()
	type tc struct {
		field      reflect.StructField
		wantName   string
		wantLayout layout
	}
	tests := []tc{
		{reflect.StructField{Name: "hidden", PkgPath: "p", Type: reflect.TypeFor[hidden](), Anonymous: true}, "", flattened},
		{reflect.StructField{Name: "Visible", Type: reflect.TypeFor[*Visible](), Anonymous: true}, "", flattened},
		{reflect.StructField{Name: "hiddenNamed", PkgPath: "p", Type: reflect.TypeFor[hidden](), Anonymous: true, Tag: `json:"in"`}, "in", tagged},
		{reflect.StructField{Name: "word", PkgPath: "p", Type: reflect.TypeFor[word](), Anonymous: true}, "", skipped},
		{reflect.StructField{Name: "Word", Type: reflect.TypeFor[Word](), Anonymous: true}, "Word", untagged},
		{reflect.StructField{Name: "Named", Type: reflect.TypeFor[Visible](), Tag: `json:"named"`}, "named", tagged},
		{reflect.StructField{Name: "Skipped", Type: stringType, Tag: `json:"-"`}, "", skipped},
		{reflect.StructField{Name: "Dash", Type: stringType, Tag: `json:"-,"`}, "-", tagged},
		{reflect.StructField{Name: "unexposed", PkgPath: "p", Type: stringType}, "", skipped},
		{reflect.StructField{Name: "Plain", Type: stringType}, "Plain", untagged},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		name, isTagged, promoted, visible := jsonName(c.field)
		if got := layoutOf(isTagged, promoted, visible); name != c.wantName || got != c.wantLayout {
			t.Errorf("jsonName(%s) = %q, %s; want %q, %s", c.field.Name, name, got, c.wantName, c.wantLayout)
		}
	}
	for _, c := range tests {
		t.Run(c.field.Name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// String names the layout in a failure message.
func (l layout) String() string {
	return [...]string{"skipped", "flattened", "untagged", "tagged"}[l]
}

// layoutOf folds jsonName's three booleans into the layout they describe.
func layoutOf(isTagged, promoted, visible bool) layout {
	switch {
	case !visible:
		return skipped
	case promoted:
		return flattened
	case isTagged:
		return tagged
	default:
		return untagged
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

// TestDeepNestingStaysWithinTheBound pins the copier's accounting against a
// document that is all structure: every container opened reserves its closer,
// and a value is only started with room for the smallest cut one, so no depth
// and no bound makes the output longer than the bound.
func TestDeepNestingStaysWithinTheBound(t *testing.T) {
	t.Parallel()
	r := NewRedactor(Config{})
	//: depths well past what any bound can hold.
	for depth := 1; depth <= 64; depth += 9 {
		document := strings.Repeat("[", depth) + `"x"` + strings.Repeat("]", depth)
		//: every bound from below the floor to past the document.
		for limit := 1; limit <= 2*depth+8; limit++ {
			out, err := r.redact([]byte(document), nil, limit)
			//: a well-formed document is never refused.
			if err != nil {
				t.Fatalf("depth %d, bound %d: %v", depth, limit, err)
			}
			//: never longer than the bound it was given.
			if len(out.JSON) > bound(limit) {
				t.Errorf("depth %d, bound %d: %d bytes: %s", depth, limit, len(out.JSON), out.JSON)
			}
		}
	}
}
