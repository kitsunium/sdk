// Package toml_test — decoding: the Go values each TOML kind becomes, typed
// targets, and the refusals, located and never quoting the document.
package toml_test

import (
	"math"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/toml"
)

// fieldOf returns the string value of the field named key on err, or "".
func fieldOf(err error, key string) string {
	//: every field along the chain.
	for _, f := range errs.FieldsOf(err) {
		//: the first with this key.
		if f.Key() == key {
			return f.StringValue()
		}
	}
	return ""
}

// decodeMap decodes doc into a fresh map[string]any.
func decodeMap(t *testing.T, doc string) map[string]any {
	t.Helper()
	var out map[string]any
	//: a document the test expects to decode.
	if err := toml.New().Unmarshal([]byte(doc), &out); err != nil {
		t.Fatalf("Unmarshal(%q) error = %v %v", doc, err, errs.FieldsOf(err))
	}
	return out
}

// TestUntypedKinds pins the Go value every TOML kind decodes to in an untyped
// target.
func TestUntypedKinds(t *testing.T) {
	t.Parallel()
	doc := `s = "x"
i = 42
f = 1.5
b = true
odt = 1979-05-27T07:32:00.5-07:00
ldt = 1979-05-27T07:32:00.123
ld = 1979-05-27
lt = 07:32:00.000001
arr = [1, "two"]
tbl = {k = 1}
[[aot]]
x = 1
`
	got := decodeMap(t, doc)
	type tc struct {
		key  string
		want any
	}
	tests := []tc{
		{"s", "x"},
		{"i", int64(42)},
		{"f", 1.5},
		{"b", true},
		{"ldt", toml.LocalDateTime{Year: 1979, Month: 5, Day: 27, Hour: 7, Minute: 32, Nanosecond: 123000000, Precision: 3}},
		{"ld", toml.LocalDate{Year: 1979, Month: 5, Day: 27}},
		{"lt", toml.LocalTime{Hour: 7, Minute: 32, Nanosecond: 1000, Precision: 6}},
		{"arr", []any{int64(1), "two"}},
		{"tbl", map[string]any{"k": int64(1)}},
		{"aot", []any{map[string]any{"x": int64(1)}}},
	}
	//: each kind compares exactly.
	for _, tc := range tests {
		//: the Go value and its type.
		if !reflect.DeepEqual(got[tc.key], tc.want) {
			t.Errorf("%s = %#v, want %#v", tc.key, got[tc.key], tc.want)
		}
	}
	wantInstant := time.Date(1979, 5, 27, 7, 32, 0, 500000000, time.FixedZone("", -7*3600))
	//: the offset date-time compares as an instant.
	if odt, ok := got["odt"].(time.Time); !ok || !odt.Equal(wantInstant) {
		t.Errorf("odt = %#v, want %v", got["odt"], wantInstant)
	}
	//: nothing else was decoded.
	if len(got) != len(tests)+1 {
		t.Errorf("decoded %d keys, want %d", len(got), len(tests)+1)
	}
}

// TestOffsetZones pins the zone an offset date-time is given: UTC for a zero
// offset however it is spelled, a fixed zone otherwise.
func TestOffsetZones(t *testing.T) {
	t.Parallel()
	got := decodeMap(t, "z = 2024-01-01T00:00:00Z\np = 2024-01-01T00:00:00+00:00\nm = 2024-01-01T00:00:00-00:00\no = 2024-01-01T00:00:00+05:30\n")
	//: the three spellings of a zero offset.
	for _, key := range []string{"z", "p", "m"} {
		//: UTC itself.
		if tm, _ := got[key].(time.Time); tm.Location() != time.UTC {
			t.Errorf("%s: location = %v, want UTC", key, tm.Location())
		}
	}
	//: a non-zero offset.
	if tm, _ := got["o"].(time.Time); func() bool { _, off := tm.Zone(); return off != 5*3600+30*60 }() {
		t.Errorf("o: zone offset wrong: %v", tm)
	}
}

// TestMergesIntoExistingMap pins that a decode into a populated map keeps the
// keys the document does not mention and merges a table into a map already
// there.
func TestMergesIntoExistingMap(t *testing.T) {
	t.Parallel()
	out := map[string]any{"keep": 1, "server": map[string]any{"port": int64(80)}}
	//: two keys of the document land beside the existing ones.
	if err := toml.New().Unmarshal([]byte("[server]\nhost = 'h'\n"), &out); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"keep": 1, "server": map[string]any{"port": int64(80), "host": "h"}}
	//: nothing lost.
	if !reflect.DeepEqual(out, want) {
		t.Errorf("merged = %#v, want %#v", out, want)
	}
	var anyTarget any = map[string]any{"keep": true}
	//: an any holding a map merges the same way.
	if err := toml.New().Unmarshal([]byte("x = 1\n"), &anyTarget); err != nil {
		t.Fatal(err)
	}
	//: both keys.
	if m, _ := anyTarget.(map[string]any); m["keep"] != true || m["x"] != int64(1) {
		t.Errorf("any target = %#v", anyTarget)
	}
}

// typed is a target exercising the typed decode.
type typed struct {
	Name     string            `toml:"name"`
	Count    int8              `toml:"count"`
	Unsigned uint16            `toml:"unsigned"`
	Ratio    float32           `toml:"ratio"`
	Whole    float64           `toml:"whole"`
	Flag     bool              `toml:"flag"`
	When     time.Time         `toml:"when"`
	Day      time.Time         `toml:"day"`
	LocalDay toml.LocalDate    `toml:"local_day"`
	Tags     []string          `toml:"tags"`
	Fixed    [2]int            `toml:"fixed"`
	Ptr      *int              `toml:"ptr"`
	Nested   *typedNested      `toml:"nested"`
	Items    []typedNested     `toml:"items"`
	Counts   map[string]int    `toml:"counts"`
	ByNumber map[int]string    `toml:"by_number"`
	Address  net.IP            `toml:"address"`
	Any      any               `toml:"any"`
	Extra    map[string]string `toml:"-"`
	Untagged string            `json:"untagged"`
	typedEmbedded
}

// typedNested is a nested table.
type typedNested struct {
	ID int `toml:"id"`
}

// typedEmbedded is flattened into typed.
type typedEmbedded struct {
	Inherited string `toml:"inherited"`
}

// TestTypedDecode decodes into a struct of every supported field shape.
func TestTypedDecode(t *testing.T) {
	t.Parallel()
	doc := `name = "n"
count = -8
unsigned = 60000
ratio = 1.5
whole = 3
flag = true
when = 2024-01-15T09:30:00Z
day = 2024-01-15
local_day = 2024-02-29
tags = ["a", "b"]
fixed = [1, 2, 3]
ptr = 7
UNTAGGED = "case-insensitive"
inherited = "from the embedded struct"
unknown = "ignored"
address = "10.0.0.1"
any = [1, {two = 2}]
"-" = "not the skipped field"
nested.id = 9
counts = {a = 1, b = 2}
by_number = {1 = "one", 22 = "twenty-two"}

[[items]]
id = 1

[[items]]
id = 2
`
	var got typed
	//: the document decodes.
	if err := toml.New().Unmarshal([]byte(doc), &got); err != nil {
		t.Fatalf("Unmarshal error = %v %v", err, errs.FieldsOf(err))
	}
	want := typed{
		Name: "n", Count: -8, Unsigned: 60000, Ratio: 1.5, Whole: 3, Flag: true,
		When:     time.Date(2024, 1, 15, 9, 30, 0, 0, time.UTC),
		Day:      time.Date(2024, 1, 15, 0, 0, 0, 0, time.Local),
		LocalDay: toml.LocalDate{Year: 2024, Month: 2, Day: 29},
		Tags:     []string{"a", "b"}, Fixed: [2]int{1, 2}, Ptr: new(7),
		Nested:    &typedNested{ID: 9},
		Items:     []typedNested{{ID: 1}, {ID: 2}},
		Counts:    map[string]int{"a": 1, "b": 2},
		ByNumber:  map[int]string{1: "one", 22: "twenty-two"},
		Address:   net.ParseIP("10.0.0.1"),
		Any:       []any{int64(1), map[string]any{"two": int64(2)}},
		Untagged:  "case-insensitive",
		Inherited: "from the embedded struct",
	}
	//: every field as written, the extra element of a fixed array dropped.
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decoded\n%+v\nwant\n%+v", got, want)
	}
}

// shouting is a string kind with an UnmarshalText of its own.
type shouting string

// UnmarshalText upper-cases the text.
func (s *shouting) UnmarshalText(text []byte) error {
	*s = shouting(strings.ToUpper(string(text)))
	return nil
}

// counted is an integer kind with an UnmarshalText of its own.
type counted int

// UnmarshalText reads every text as 99.
func (c *counted) UnmarshalText([]byte) error {
	*c = 99
	return nil
}

// TestNativeKindsBeforeUnmarshalText pins the replaced library's precedence:
// a value goes into a kind that holds it directly, and UnmarshalText is the
// fallback for the kinds that do not — a string-kinded type is set from a
// TOML string as it is, an integer-kinded one from a TOML integer.
func TestNativeKindsBeforeUnmarshalText(t *testing.T) {
	t.Parallel()
	var got struct {
		Name     shouting         `toml:"name"`
		Count    counted          `toml:"count"`
		FromText counted          `toml:"from_text"`
		When     time.Time        `toml:"when"`
		ByName   map[shouting]int `toml:"by_name"`
	}
	doc := "name = 'quiet'\ncount = 7\nfrom_text = 'seven'\nwhen = '2024-01-15T09:30:00Z'\nby_name = {low = 1}\n"
	//: decodes.
	if err := toml.New().Unmarshal([]byte(doc), &got); err != nil {
		t.Fatal(err)
	}
	//: the native kinds are assigned as written.
	if got.Name != "quiet" || got.Count != 7 || got.ByName["low"] != 1 {
		t.Errorf("native kinds: %+v", got)
	}
	//: a value of another kind goes through UnmarshalText.
	if got.FromText != 99 || !got.When.Equal(time.Date(2024, 1, 15, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("through UnmarshalText: %+v", got)
	}
}

// TestInlineTablesReplaceStandardTablesMerge pins the replaced library's
// rule for a target that already holds a map: a table a header or a dotted
// key defines is merged into it, an inline table replaces it, untyped or
// typed.
func TestInlineTablesReplaceStandardTablesMerge(t *testing.T) {
	t.Parallel()
	untyped := map[string]any{
		"inline":   map[string]any{"kept": true},
		"standard": map[string]any{"kept": true},
		"dotted":   map[string]any{"kept": true},
	}
	doc := "inline = {added = 1}\ndotted.added = 1\n[standard]\nadded = 1\n"
	//: decodes over the existing maps.
	if err := toml.New().Unmarshal([]byte(doc), &untyped); err != nil {
		t.Fatal(err)
	}
	//: the inline table replaced its map.
	if inline, _ := untyped["inline"].(map[string]any); len(inline) != 1 || inline["added"] != int64(1) {
		t.Errorf("inline = %#v, want only the document's key", untyped["inline"])
	}
	//: the standard and dotted tables merged into theirs.
	for _, key := range []string{"standard", "dotted"} {
		//: both keys.
		if m, _ := untyped[key].(map[string]any); len(m) != 2 {
			t.Errorf("%s = %#v, want the kept key and the added one", key, untyped[key])
		}
	}
	typed := struct {
		Inline   map[string]int `toml:"inline"`
		Standard map[string]int `toml:"standard"`
	}{Inline: map[string]int{"kept": 1}, Standard: map[string]int{"kept": 1}}
	//: the same rule into typed maps.
	if err := toml.New().Unmarshal([]byte("inline = {added = 2}\n[standard]\nadded = 2\n"), &typed); err != nil {
		t.Fatal(err)
	}
	//: replaced, merged.
	if len(typed.Inline) != 1 || len(typed.Standard) != 2 {
		t.Errorf("typed = %+v", typed)
	}
}

// TestGoArrays pins how a fixed-length Go array is filled: elements past its
// length are dropped from a static array, an array of tables longer than it
// is refused, and the elements the document does not reach are left alone.
func TestGoArrays(t *testing.T) {
	t.Parallel()
	got := struct {
		Short [2]int         `toml:"short"`
		Long  [3]int         `toml:"long"`
		Items [1]typedNested `toml:"items"`
	}{Long: [3]int{7, 8, 9}}
	//: three into two, one into three.
	if err := toml.New().Unmarshal([]byte("short = [1, 2, 3]\nlong = [1]\n"), &got); err != nil {
		t.Fatal(err)
	}
	//: dropped, and left alone.
	if got.Short != [2]int{1, 2} || got.Long != [3]int{1, 8, 9} {
		t.Errorf("short = %v, long = %v", got.Short, got.Long)
	}
	err := toml.New().Unmarshal([]byte("[[items]]\nid = 1\n[[items]]\nid = 2\n"), &got)
	//: a second table has nowhere to go.
	if fieldOf(err, "problem") != "the array of tables has more elements than the Go array holds" {
		t.Errorf("err = %v %v", err, errs.FieldsOf(err))
	}
}

// TestTablesIntoSlicesAndArrays pins where a table lands when its field is a
// slice or an array, as the replaced library placed it: a slice's last
// element, appended when there is none; an array's first element, but only
// for a table a longer header or a dotted key passes through.
func TestTablesIntoSlicesAndArrays(t *testing.T) {
	t.Parallel()
	type holder struct {
		Items []typedNested  `toml:"items"`
		Fixed [2]typedNested `toml:"fixed"`
	}
	var got holder
	//: a [table] into an empty slice, a dotted key through an array.
	if err := toml.New().Unmarshal([]byte("fixed.id = 4\n[items]\nid = 3\n"), &got); err != nil {
		t.Fatal(err)
	}
	//: the appended element, the first element.
	if len(got.Items) != 1 || got.Items[0].ID != 3 || got.Fixed[0].ID != 4 {
		t.Errorf("got %+v", got)
	}
	got = holder{Items: []typedNested{{ID: 1}, {ID: 2}}}
	//: a [table] into a slice that has elements fills the last one.
	if err := toml.New().Unmarshal([]byte("[items]\nid = 9\n"), &got); err != nil || got.Items[1].ID != 9 || got.Items[0].ID != 1 {
		t.Errorf("got %+v, %v", got, err)
	}
	//: a table defined by its own header, or inline, cannot land on an array.
	for _, doc := range []string{"[fixed]\nid = 1\n", "fixed = {id = 1}\n", "items = {id = 1}\n"} {
		err := toml.New().Unmarshal([]byte(doc), &got)
		//: refused.
		if fieldOf(err, "problem") != "the value does not fit the target's type" {
			t.Errorf("%q: err = %v %v", doc, err, errs.FieldsOf(err))
		}
	}
}

// TestDatesOnlyIntoTimes refuses a date or a time into anything but
// time.Time and the local types, even a type with an UnmarshalText, as the
// replaced library refused it.
func TestDatesOnlyIntoTimes(t *testing.T) {
	t.Parallel()
	var got struct {
		Name shouting `toml:"name"`
	}
	err := toml.New().Unmarshal([]byte("name = 1979-05-27\n"), &got)
	//: refused, naming the field.
	if fieldOf(err, "problem") != "the value does not fit the target's type" || fieldOf(err, "key") != "name" {
		t.Errorf("err = %v %v", err, errs.FieldsOf(err))
	}
}

// TestExactMatchWinsOverFolded pins that a key matching a field exactly is not
// taken by another field that matches it without regard to case.
func TestExactMatchWinsOverFolded(t *testing.T) {
	t.Parallel()
	var got struct {
		Lower string `toml:"key"`
		Upper string `toml:"KEY"`
	}
	//: two keys, each with its own field.
	if err := toml.New().Unmarshal([]byte("KEY = 'upper'\nkey = 'lower'\n"), &got); err != nil {
		t.Fatal(err)
	}
	//: no cross-talk.
	if got.Lower != "lower" || got.Upper != "upper" {
		t.Errorf("got %+v", got)
	}
}

// TestRefusals pins every class of refusal: UNMARSHAL_FAILED, the problem
// named, and the place located.
func TestRefusals(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		doc     string
		target  any
		problem string
		key     string
		line    string
	}
	tests := []tc{
		{"syntax", "a = 1\n= bad\n", new(map[string]any), "expected a key", "", "2"},
		{"duplicate key", "a = 1\na = 2\n", new(map[string]any), "the key is already defined", "", "2"},
		{"table twice", "[t]\n[t]\n", new(map[string]any), "the table is already defined", "", "2"},
		{"dotted then header", "a.b = 1\n[a]\n", new(map[string]any), "the table was defined by dotted keys and cannot be defined again", "", "2"},
		{"array of tables then table", "[[a]]\n[a]\n", new(map[string]any), "the table is already an array of tables", "", "2"},
		{"static array appended", "a = []\n[[a]]\n", new(map[string]any), "the key is already defined and is not an array of tables", "", "2"},
		{"inline table extended", "a = {b = 1}\n[a.c]\n", new(map[string]any), "the key is a value, a static array or an inline table, and cannot be extended", "", "2"},
		{"dotted into header table", "[a.b]\n[a]\nb.c = 1\n", new(map[string]any), "a dotted key cannot extend a table defined by a header, a value or an inline table", "", "3"},
		{"integer range", "a = 9223372036854775808\n", new(map[string]any), "the integer does not fit a signed 64-bit integer", "", "1"},
		{"leading zero", "a = 012\n", new(map[string]any), "a decimal number cannot have a leading zero", "", "1"},
		{"impossible date", "a = 2023-02-29\n", new(map[string]any), "malformed or impossible date", "", "1"},
		{"leap second", "a = 23:59:60\n", new(map[string]any), "malformed or impossible time", "", "1"},
		{"control in comment", "# a\x01b\n", new(map[string]any), "a comment contains a control character", "", "1"},
		{"invalid UTF-8", "a = '\xff'\n", new(map[string]any), "the document is not valid UTF-8", "", "1"},
		{"bad escape", `a = "\q"` + "\n", new(map[string]any), "invalid escape sequence", "", "1"},
		{"surrogate escape", `a = "\uD800"` + "\n", new(map[string]any), "invalid escape sequence", "", "1"},
		{"out of range escape", `a = "\UFFFFFFFF"` + "\n", new(map[string]any), "invalid escape sequence", "", "1"},
		{"overflow int8", "count = 128\n", new(typed), "the number does not fit the target's type", "count", "1"},
		{"negative unsigned", "unsigned = -1\n", new(typed), "the number does not fit the target's type", "unsigned", "1"},
		{"float32 overflow", "ratio = 3.5e38\n", new(typed), "the number does not fit the target's type", "ratio", "1"},
		{"string into int", "count = 'x'\n", new(typed), "the value does not fit the target's type", "count", "1"},
		{"float into int", "count = 1.0\n", new(typed), "the value does not fit the target's type", "count", "1"},
		{"table into string", "[name]\n", new(typed), "the value does not fit the target's type", "name", "1"},
		{"nested mismatch", "[[items]]\nid = 'x'\n", new(typed), "the value does not fit the target's type", "items.id", "2"},
		{"offset into local", "local_day = 2024-01-01T00:00:00Z\n", new(typed), "the value does not fit the target's type", "local_day", "1"},
		{"bad map key", "by_number = {one = 'x'}\n", new(typed), "the key does not fit the map's key type", "by_number.one", "1"},
		{"text refused", "address = 'not an ip'\n", new(typed), "the target's UnmarshalText refused the value", "address", "1"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := toml.New().Unmarshal([]byte(tc.doc), tc.target)
		//: every refusal is UNMARSHAL_FAILED.
		if !errs.HasReason(err, "UNMARSHAL_FAILED") {
			t.Fatalf("%s: err = %v, want UNMARSHAL_FAILED", tc.name, err)
		}
		//: the problem is named.
		if got := fieldOf(err, "problem"); got != tc.problem {
			t.Errorf("%s: problem = %q, want %q", tc.name, got, tc.problem)
		}
		//: the key is named when there is one.
		if got := fieldOf(err, "key"); got != tc.key {
			t.Errorf("%s: key = %q, want %q", tc.name, got, tc.key)
		}
		//: and the line.
		if got := fieldOf(err, "line"); got != tc.line {
			t.Errorf("%s: line = %q, want %q", tc.name, got, tc.line)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestRefusalNeverQuotesTheDocument pins that no part of a refusal — its
// message, its private text, its fields — carries a byte of the document,
// whose values are where secrets live.
func TestRefusalNeverQuotesTheDocument(t *testing.T) {
	t.Parallel()
	const secret = "hunter2-SECRET-value"
	docs := []string{
		"password = '" + secret + "'\npassword = 'again'\n",
		"password = \"" + secret + "\\q\"\n",
		"[" + "db" + "]\npassword = '" + secret + "' trailing\n",
		"count = '" + secret + "'\n",
	}
	//: each refused document.
	for _, doc := range docs {
		err := toml.New().Unmarshal([]byte(doc), new(typed))
		//: refused.
		if err == nil {
			t.Fatalf("%q decoded", doc)
		}
		rendered := err.Error() + errs.PrivateOf(err)
		//: every field too.
		for _, f := range errs.FieldsOf(err) {
			rendered += f.Key() + "=" + f.StringValue()
		}
		//: no trace of the value.
		if strings.Contains(rendered, secret) || strings.Contains(rendered, "hunter2") {
			t.Errorf("the refusal of %q carries the secret: %s", doc, rendered)
		}
	}
}

// TestTargetMustBeAPointer refuses a target the decoder cannot write to.
func TestTargetMustBeAPointer(t *testing.T) {
	t.Parallel()
	var nilMap *map[string]any
	//: a value, a nil pointer and nil.
	for _, target := range []any{map[string]any{}, nilMap, nil} {
		err := toml.New().Unmarshal([]byte("a = 1\n"), target)
		//: refused, naming the problem.
		if fieldOf(err, "problem") != "the target is not a non-nil pointer" {
			t.Errorf("target %T: err = %v", target, err)
		}
	}
}

// TestEmptyDocument decodes nothing into an empty, non-nil map.
func TestEmptyDocument(t *testing.T) {
	t.Parallel()
	//: an empty document, and one of blank lines and comments.
	for _, doc := range []string{"", "\n\n", "# only a comment\n", " \t\r\n"} {
		got := decodeMap(t, doc)
		//: an empty table.
		if got == nil || len(got) != 0 {
			t.Errorf("%q: decoded %#v", doc, got)
		}
	}
}

// TestVersion11Relaxations pins the TOML v1.1.0 forms the codec accepts
// because the library it replaced accepted them.
func TestVersion11Relaxations(t *testing.T) {
	t.Parallel()
	doc := "t = {\n  a = 1, # a comment\n  b = 2,\n}\ne = \"\\e\"\nx = \"\\x41\"\nlt = 07:32\nldt = 1979-05-27T07:32\n"
	got := decodeMap(t, doc)
	type tc struct {
		key  string
		want any
	}
	tests := []tc{
		{"t", map[string]any{"a": int64(1), "b": int64(2)}},
		{"e", "\x1b"},
		{"x", "A"},
		{"lt", toml.LocalTime{Hour: 7, Minute: 32}},
		{"ldt", toml.LocalDateTime{Year: 1979, Month: 5, Day: 27, Hour: 7, Minute: 32}},
	}
	//: decoded as TOML v1.1.0 reads them.
	for _, tc := range tests {
		//: each relaxation.
		if !reflect.DeepEqual(got[tc.key], tc.want) {
			t.Errorf("%s = %#v, want %#v", tc.key, got[tc.key], tc.want)
		}
	}
}

// TestNestingCap pins that values nest up to maxDepth and no deeper, in both
// a tables chain and an array chain.
func TestNestingCap(t *testing.T) {
	t.Parallel()
	const depth int = 128
	type tc struct {
		name string
		doc  string
		ok   bool
	}
	keys := func(n int) string { return strings.Repeat("a.", n-1) + "a" }
	tests := []tc{
		{"array at the cap", "a = " + strings.Repeat("[", depth) + strings.Repeat("]", depth) + "\n", true},
		{"array past the cap", "a = " + strings.Repeat("[", depth+1) + strings.Repeat("]", depth+1) + "\n", false},
		{"value past the cap", "a = " + strings.Repeat("[", depth) + "1" + strings.Repeat("]", depth) + "\n", false},
		{"header at the cap", "[" + keys(depth) + "]\n", true},
		{"header past the cap", "[" + keys(depth+1) + "]\n", false},
		{"dotted key past the cap", keys(depth+1) + " = 1\n", false},
		{"inline tables past the cap", "a = " + strings.Repeat("{b = ", depth) + "1" + strings.Repeat("}", depth) + "\n", false},
		{"ten thousand brackets", "a = " + strings.Repeat("[", 10000) + "\n", false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var out map[string]any
		err := toml.New().Unmarshal([]byte(tc.doc), &out)
		//: within the cap, decoded.
		if tc.ok {
			if err != nil {
				t.Fatalf("%s: %v %v", tc.name, err, errs.FieldsOf(err))
			}
			return
		}
		//: past it, refused as too deep.
		if fieldOf(err, "problem") != "tables and arrays are nested too deep" {
			t.Errorf("%s: err = %v %v", tc.name, err, errs.FieldsOf(err))
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestSizeCap refuses a document larger than the cap before reading it, and
// admits one of exactly the cap.
//
// The cap is admitted twice over. With no target, the refusal that follows
// names the target and not the size: that is the size check alone, at no
// cost, in every build. Outside a coverage build the whole 10 MiB document is
// also decoded. In a coverage build each basic block bumps a counter, and
// under the race detector, which `bazel coverage` keeps on, every bump is an
// instrumented atomic: that decode took 15.9 s of the 60 s .bazelrc gives the
// target (2.5 s under the race detector alone). The race suite, the alloc lane
// and `go test` still decode it.
func TestSizeCap(t *testing.T) {
	t.Parallel()
	doc := make([]byte, 10<<20+1)
	//: blank lines: valid TOML, only too long.
	for i := range doc {
		doc[i] = '\n'
	}
	var out map[string]any
	err := toml.New().Unmarshal(doc, &out)
	//: refused, naming the bound.
	if fieldOf(err, "problem") != "the document is larger than the limit" || fieldOf(err, "limit") != "10485760" {
		t.Errorf("err = %v %v", err, errs.FieldsOf(err))
	}
	//: one byte under the cap passes the size check: what refuses it is the missing target.
	if err := toml.New().Unmarshal(doc[:10<<20], nil); fieldOf(err, "problem") != "the target is not a non-nil pointer" {
		t.Errorf("at the cap, with no target: err = %v %v", err, errs.FieldsOf(err))
	}
	//: and it decodes, wherever a 10 MiB decode fits the budget.
	if testing.CoverMode() == "" {
		if err := toml.New().Unmarshal(doc[:10<<20], &out); err != nil {
			t.Errorf("at the cap: %v", err)
		}
	}
}

// TestWideTable decodes a table wide enough to move to the hash index, and
// refuses a duplicate key found through it.
func TestWideTable(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	//: a thousand keys in one table.
	for i := range 1000 {
		sb.WriteString("k")
		sb.WriteString(strings.Repeat("x", i%7))
		sb.WriteString(itoa(i))
		sb.WriteString(" = 1\n")
	}
	doc := sb.String()
	//: all of them, distinct.
	if got := decodeMap(t, doc); len(got) != 1000 {
		t.Fatalf("decoded %d keys", len(got))
	}
	var out map[string]any
	err := toml.New().Unmarshal([]byte(doc+"kxxxxx5 = 2\n"), &out)
	//: a key repeated after the table went wide is still a duplicate.
	if fieldOf(err, "problem") != "the key is already defined" || fieldOf(err, "line") != "1001" {
		t.Errorf("err = %v %v", err, errs.FieldsOf(err))
	}
}

// itoa formats a small non-negative integer.
func itoa(i int) string {
	//: zero is a digit too.
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

// TestFloatsAndSpecials decodes the special floats and the edge integers.
func TestFloatsAndSpecials(t *testing.T) {
	t.Parallel()
	got := decodeMap(t, "a = inf\nb = -inf\nc = nan\nd = -0.0\ne = 1e-400\nf = 0x7FFF_FFFF_FFFF_FFFF\ng = -9223372036854775808\nh = 0o777\ni = 0b1010\nj = 1_000.5e1_0\n")
	//: the infinities.
	if !math.IsInf(got["a"].(float64), 1) || !math.IsInf(got["b"].(float64), -1) {
		t.Errorf("infinities: %v %v", got["a"], got["b"])
	}
	//: NaN.
	if !math.IsNaN(got["c"].(float64)) {
		t.Errorf("nan: %v", got["c"])
	}
	//: negative zero keeps its sign.
	if d := got["d"].(float64); d != 0 || !math.Signbit(d) {
		t.Errorf("-0.0: %v", d)
	}
	//: an underflow rounds to zero.
	if got["e"].(float64) != 0 {
		t.Errorf("underflow: %v", got["e"])
	}
	var largest, smallest int64 = math.MaxInt64, math.MinInt64
	//: the integer extremes and radixes.
	if got["f"] != largest || got["g"] != smallest || got["h"] != int64(511) || got["i"] != int64(10) {
		t.Errorf("integers: %v %v %v %v", got["f"], got["g"], got["h"], got["i"])
	}
	//: underscores in a float.
	if got["j"] != 1000.5e10 {
		t.Errorf("float with underscores: %v", got["j"])
	}
}
