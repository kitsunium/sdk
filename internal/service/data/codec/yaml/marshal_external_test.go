package yaml_test

import (
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/yaml"
)

// goldenDocument is what the encoder writes for goldenValue, byte for byte.
const goldenDocument string = `map:
  a:
    - true
    - null
    - x
  m: {}
  z: 1.5
int_keys:
  2: two
  10: ten
title: hello world
desc: |
  line one
  line two
lead: |2-
    lead
  text
inner:
  name: in
  tags: [a, b c, "d,e"]
list:
  - name: x
    tags: []
  - name: "y"
    tags: []
matrix:
  - - 1
    - 2
  - - 3
bytes: [0, 1, 255]
when: "2024-01-15T09:30:00Z"
wait: 1m30s
count: 3
ratio: 1.0
nil: null
`

// The extremes of the 64-bit integers, as typed constants: an untyped
// constant in an any would become an int, or not compile.
const (
	// minInt64 is math.MinInt64 as an int64.
	minInt64 int64 = math.MinInt64
	// maxInt64 is math.MaxInt64 as an int64.
	maxInt64 int64 = math.MaxInt64
	// maxUint64 is math.MaxUint64 as a uint64.
	maxUint64 uint64 = math.MaxUint64
)

// untypedInt is what an untyped decode yields for the integer i, by yaml.v3's
// own rule — an int when i fits one, the int64 itself when it does not — so
// an expectation holds on every word size. The 64-bit extremes are an int
// where an int is 64 bits wide and an int64 where it is 32: spelled
// math.MaxInt or math.MinInt, the expectation is a 32-bit value on linux/386
// while the decoder, rightly, returns the exact 64-bit one.
func untypedInt(i int64) any {
	//: an int holds i on this platform.
	if int64(int(i)) == i {
		//: int.
		return int(i)
	}
	//: an int is narrower than i: the int64.
	return i
}

// goldenInner is a nested struct of goldenOuter.
type goldenInner struct {
	// Name is a plain key.
	Name string `yaml:"name"`
	// Tags is written in flow style.
	Tags []string `yaml:"tags,flow"`
}

// goldenOuter covers the shapes the encoder lays out.
type goldenOuter struct {
	// Map is an untyped mapping.
	Map map[string]any `yaml:"map"`
	// IntKeys is a mapping with integer keys, sorted numerically.
	IntKeys map[int]string `yaml:"int_keys"`
	// Title is a plain string.
	Title string `yaml:"title"`
	// Skip is left out when empty.
	Skip string `yaml:"skip,omitempty"`
	// Desc is a multi-line string, written as a literal block.
	Desc string `yaml:"desc"`
	// Lead is a multi-line string whose first line starts with a blank.
	Lead string `yaml:"lead"`
	// Inner is a nested struct.
	Inner goldenInner `yaml:"inner"`
	// List is a sequence of structs, written compact.
	List []goldenInner `yaml:"list"`
	// Matrix is a sequence of sequences.
	Matrix [][]int `yaml:"matrix"`
	// Bytes is written as a flow sequence of integers.
	Bytes []byte `yaml:"bytes"`
	// When is written as RFC 3339 text, quoted.
	When time.Time `yaml:"when"`
	// Wait is written as a duration's text.
	Wait time.Duration `yaml:"wait"`
	// Count is an integer.
	Count int `yaml:"count"`
	// Ratio is a float, written with its dot.
	Ratio float64 `yaml:"ratio"`
	// Nil is a nil pointer.
	Nil *int `yaml:"nil"`
}

// goldenValue returns the goldenOuter the golden document encodes.
func goldenValue() goldenOuter {
	//: every field set but Skip.
	return goldenOuter{
		Title: "hello world", Count: 3, Ratio: 1,
		Inner:   goldenInner{Name: "in", Tags: []string{"a", "b c", "d,e"}},
		List:    []goldenInner{{Name: "x"}, {Name: "y", Tags: []string{}}},
		Matrix:  [][]int{{1, 2}, {3}},
		Desc:    "line one\nline two\n",
		Lead:    "  lead\ntext",
		Map:     map[string]any{"z": 1.5, "a": []any{true, nil, "x"}, "m": map[string]any{}},
		When:    time.Date(2024, 1, 15, 9, 30, 0, 0, time.UTC),
		Wait:    90 * time.Second,
		Bytes:   []byte{0, 1, 255},
		IntKeys: map[int]string{10: "ten", 2: "two"},
	}
}

// TestMarshalWritesDeterministicBlockStyle compares the encoder's output with
// the golden document, twice, and decodes it back to the same value.
func TestMarshalWritesDeterministicBlockStyle(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"first"}, {"second, same bytes"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out, err := yaml.New().Marshal(goldenValue())
		if err != nil {
			t.Fatalf("%s: Marshal error = %v", tc.name, err)
		}
		if string(out) != goldenDocument {
			t.Fatalf("%s: Marshal =\n%s\nwant\n%s", tc.name, out, goldenDocument)
		}
		var back goldenOuter
		if err := yaml.New().Unmarshal(out, &back); err != nil {
			t.Fatalf("%s: Unmarshal(golden) error = %v", tc.name, err)
		}
		want := goldenValue()
		//: an empty flow sequence decodes to an empty slice, not nil.
		want.List[0].Tags = []string{}
		if !reflect.DeepEqual(back, want) {
			t.Errorf("%s: round trip = %#v, want %#v", tc.name, back, want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestStringsReadBackAsTheSameString writes strings that a reader would take
// for something else unless quoted, and strings that need no quoting, and
// checks how each is written and that each decodes back unchanged.
func TestStringsReadBackAsTheSameString(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		value string
		want  string
	}
	tests := []tc{
		{"plain word", "kitsune", "kitsune\n"},
		{"plain with spaces", "hello world", "hello world\n"},
		{"plain with inner hash", "a#b", "a#b\n"},
		{"plain duration", "30s", "30s\n"},
		{"plain unit", "1.5GB", "1.5GB\n"},
		{"empty", "", "\"\"\n"},
		{"core null", "null", "\"null\"\n"},
		{"tilde", "~", "\"~\"\n"},
		{"core boolean", "true", "\"true\"\n"},
		{"YAML 1.1 boolean", "yes", "\"yes\"\n"},
		{"YAML 1.1 short boolean", "n", "\"n\"\n"},
		{"integer", "8080", "\"8080\"\n"},
		{"octal-looking", "0644", "\"0644\"\n"},
		{"underscored", "1_000", "\"1_000\"\n"},
		{"underscore after a sign", "+_0", "\"+_0\"\n"},
		{"underscore after a dot", "-._5", "\"-._5\"\n"},
		{"merge key text", "<<", "\"<<\"\n"},
		{"float", "1e3", "\"1e3\"\n"},
		{"infinity", ".inf", "\".inf\"\n"},
		{"timestamp", "2024-01-15", "\"2024-01-15\"\n"},
		{"leading space", " x", "\" x\"\n"},
		{"trailing space", "x ", "\"x \"\n"},
		{"value indicator", "a: b", "\"a: b\"\n"},
		{"comment", "a #b", "\"a #b\"\n"},
		{"indicator first", "-", "\"-\"\n"},
		{"anchor first", "&x", "\"&x\"\n"},
		{"document marker", "--- x", "\"--- x\"\n"},
		{"tab", "a\tb", "\"a\\tb\"\n"},
		{"control character", "a\x01b", "\"a\\x01b\"\n"},
		{"line separator", "a b", "\"a\\Lb\"\n"},
		{"multi-line", "one\ntwo\n", "|\n  one\n  two\n"},
		{"multi-line without final break", "one\ntwo", "|-\n  one\n  two\n"},
		{"multi-line keeping breaks", "one\n\n", "|+\n  one\n\n"},
		{"multi-line at the root with a leading blank", " one\ntwo", "\" one\\ntwo\"\n"},
		{"only line breaks", "\n\n", "\"\\n\\n\"\n"},
		{"carriage return", "a\r\nb", "\"a\\r\\nb\"\n"},
		{"unicode", "héllo 世界 🌸", "héllo 世界 🌸\n"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out, err := yaml.New().Marshal(tc.value)
		if err != nil {
			t.Fatalf("%s: Marshal error = %v", tc.name, err)
		}
		if string(out) != tc.want {
			t.Errorf("%s: Marshal(%q) = %q, want %q", tc.name, tc.value, out, tc.want)
		}
		var back any
		if err := yaml.New().Unmarshal(out, &back); err != nil {
			t.Fatalf("%s: Unmarshal(%q) error = %v", tc.name, out, err)
		}
		if back != tc.value {
			t.Errorf("%s: round trip = %#v, want %q", tc.name, back, tc.value)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestMergeKeyTextIsAQuotedKey writes a map keyed by the text << and checks
// the key is quoted — written plain, the subset refuses it as a merge key and
// yaml.v3 reads it as one — and decodes back as the same key.
func TestMergeKeyTextIsAQuotedKey(t *testing.T) {
	t.Parallel()
	const want = "\"<<\": 1\n"
	out, err := yaml.New().Marshal(map[string]int{"<<": 1})
	if err != nil {
		t.Fatalf("Marshal error = %v", err)
	}
	if string(out) != want {
		t.Errorf("Marshal = %q, want %q", out, want)
	}
	var back map[string]int
	if err := yaml.New().Unmarshal(out, &back); err != nil {
		t.Fatalf("Unmarshal(%q) error = %v", out, err)
	}
	if len(back) != 1 || back["<<"] != 1 {
		t.Errorf("round trip = %#v, want map[<<:1]", back)
	}
}

// TestNumbersReadBackAsNumbers writes numbers and checks the text and that an
// untyped decode reads the same kind back: a float stays a float.
func TestNumbersReadBackAsNumbers(t *testing.T) {
	t.Parallel()
	type tc struct {
		value any
		back  any
		name  string
		want  string
	}
	tests := []tc{
		{name: "int", value: 42, want: "42\n", back: 42},
		{name: "negative int64", value: minInt64, want: "-9223372036854775808\n", back: untypedInt(minInt64)},
		{name: "uint64 past int64", value: maxUint64, want: "18446744073709551615\n", back: maxUint64},
		{name: "whole float", value: 1.0, want: "1.0\n", back: 1.0},
		{name: "big float", value: 1e21, want: "1.0e+21\n", back: 1e21},
		{name: "small float", value: 1e-7, want: "1.0e-07\n", back: 1e-7},
		{name: "float32", value: float32(3.1415927), want: "3.1415927\n", back: float64(float32(3.1415927))},
		{name: "infinity", value: math.Inf(-1), want: "-.inf\n", back: math.Inf(-1)},
		{name: "boolean", value: false, want: "false\n", back: false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out, err := yaml.New().Marshal(tc.value)
		if err != nil {
			t.Fatalf("%s: Marshal error = %v", tc.name, err)
		}
		if string(out) != tc.want {
			t.Errorf("%s: Marshal(%v) = %q, want %q", tc.name, tc.value, out, tc.want)
		}
		var back any
		if err := yaml.New().Unmarshal(out, &back); err != nil {
			t.Fatalf("%s: Unmarshal error = %v", tc.name, err)
		}
		//: float32 text is read at 64 bits by an untyped decode.
		if tc.name == "float32" {
			if f, ok := back.(float64); !ok || float32(f) != float32(3.1415927) {
				t.Errorf("%s: round trip = %#v", tc.name, back)
			}
			return
		}
		if back != tc.back {
			t.Errorf("%s: round trip = %#v, want %#v", tc.name, back, tc.back)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// tagged covers the yaml struct tag: names, "-", omitempty, the legacy bare
// tag, and an embedded struct that is not inlined.
type tagged struct {
	// Renamed takes the tag's name.
	Renamed string `yaml:"renamed_key"`
	// Hidden is left out.
	Hidden string `yaml:"-"`
	// Omitted is left out when empty.
	Omitted []string `yaml:"omitted,omitempty"`
	// ZeroTime is left out when its IsZero says so.
	ZeroTime time.Time `yaml:"zero_time,omitempty"`
	// ZeroStruct is left out when every exported field is empty.
	ZeroStruct goldenInner `yaml:"zero_struct,omitempty"`
	// Embedded is embedded, not inlined: its key is its type name in lower
	// case, as yaml.v3 names it.
	Embedded
	// unexported is never written.
	unexported int
}

// Embedded is embedded in tagged without the inline flag.
type Embedded struct {
	// Name is a key of the embedded mapping.
	Name string `yaml:"name"`
}

// TestStructTags writes and reads a struct exercising every tag form.
func TestStructTags(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"tags", "renamed_key: r\nembedded:\n  name: e\n"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		value := tagged{Renamed: "r", Hidden: "h", Name: "e", unexported: 9}
		out, err := yaml.New().Marshal(value)
		if err != nil {
			t.Fatalf("%s: Marshal error = %v", tc.name, err)
		}
		if string(out) != tc.want {
			t.Fatalf("%s: Marshal = %q, want %q", tc.name, out, tc.want)
		}
		var back tagged
		if err := yaml.New().Unmarshal([]byte(tc.want+"hidden: x\nomitted: [a]\n"), &back); err != nil {
			t.Fatalf("%s: Unmarshal error = %v", tc.name, err)
		}
		want := tagged{Renamed: "r", Omitted: []string{"a"}, Name: "e"}
		if !reflect.DeepEqual(back, want) {
			t.Errorf("%s: Unmarshal = %#v, want %#v", tc.name, back, want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// inlineBase is inlined into inlined.
type inlineBase struct {
	// ID is a key of the parent.
	ID int `yaml:"id"`
}

// inlineMore is inlined through a pointer.
type inlineMore struct {
	// Note is a key of the parent.
	Note string `yaml:"note"`
}

// inlined merges a struct, a pointer to a struct and a catch-all map.
type inlined struct {
	// Rest collects the keys no field declares.
	Rest map[string]any `yaml:",inline"`
	// More is inlined through a pointer.
	More *inlineMore `yaml:",inline"`
	// Name is the struct's own key.
	Name string `yaml:"name"`
	// inlineBase is inlined.
	inlineBase `yaml:",inline"`
}

// TestInline writes and reads inlined structs and an inline map.
func TestInline(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		doc  string
	}
	tests := []tc{{"merged", "note: memo\nname: x\nid: 7\nextra: 1\nother:\n  - a\n"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var got inlined
		if err := yaml.New().Unmarshal([]byte(tc.doc), &got); err != nil {
			t.Fatalf("%s: Unmarshal error = %v", tc.name, err)
		}
		want := inlined{Name: "x", ID: 7, More: &inlineMore{Note: "memo"}, Rest: map[string]any{"extra": 1, "other": []any{"a"}}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: Unmarshal = %#v, want %#v", tc.name, got, want)
		}
		out, err := yaml.New().Marshal(got)
		if err != nil {
			t.Fatalf("%s: Marshal error = %v", tc.name, err)
		}
		if string(out) != tc.doc {
			t.Errorf("%s: Marshal = %q, want %q", tc.name, out, tc.doc)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// pairOut writes itself through MarshalYAML, as a sequence of two integers.
type pairOut struct {
	// a and b are written as one sequence.
	a, b int
}

// MarshalYAML writes the pair.
func (h pairOut) MarshalYAML() (any, error) {
	//: a sequence.
	return []int{h.a, h.b}, nil
}

// pairIn reads itself through UnmarshalYAML(func(any) error), from what
// pairOut writes.
type pairIn struct {
	// a and b are read from one sequence.
	a, b int
}

// UnmarshalYAML reads the pair.
func (h *pairIn) UnmarshalYAML(unmarshal func(any) error) error {
	var pair []int
	if err := unmarshal(&pair); err != nil {
		return err
	}
	if len(pair) != 2 {
		return errHookPair
	}
	h.a, h.b = pair[0], pair[1]
	return nil
}

// errHookPair is the hook's own refusal.
var errHookPair = errors.New("a pair has two elements")

// levelOut is a TextMarshaler, used as a value and as a key.
type levelOut int

// MarshalText names the level.
func (l levelOut) MarshalText() ([]byte, error) {
	//: its name.
	return []byte("level-" + strconv.Itoa(int(l))), nil
}

// levelIn is a TextUnmarshaler reading what levelOut writes.
type levelIn int

// UnmarshalText reads the name back.
func (l *levelIn) UnmarshalText(text []byte) error {
	n, err := strconv.Atoi(strings.TrimPrefix(string(text), "level-"))
	if err != nil {
		return err
	}
	*l = levelIn(n)
	return nil
}

// foreign has yaml.v3's node-based UnmarshalYAML, which the codec refuses.
type foreign struct{}

// foreignNode stands in for *yaml.Node.
type foreignNode struct{}

// UnmarshalYAML has the signature the codec cannot call.
func (*foreign) UnmarshalYAML(_ *foreignNode) error {
	//: never called.
	return nil
}

// TestHooks writes values through MarshalYAML and MarshalText, reads them
// back through UnmarshalYAML and UnmarshalText, refuses a foreign
// UnmarshalYAML, and surfaces a hook's error.
func TestHooks(t *testing.T) {
	t.Parallel()
	type holderOut struct {
		Pair   pairOut               `yaml:"pair"`
		Levels map[levelOut]levelOut `yaml:"levels"`
		PPair  *pairOut              `yaml:"ppair"`
	}
	type holderIn struct {
		Pair   pairIn              `yaml:"pair"`
		Levels map[levelIn]levelIn `yaml:"levels"`
		PPair  *pairIn             `yaml:"ppair"`
	}
	out, err := yaml.New().Marshal(holderOut{Pair: pairOut{1, 2}, Levels: map[levelOut]levelOut{3: 4}, PPair: &pairOut{5, 6}})
	if err != nil {
		t.Fatalf("Marshal error = %v", err)
	}
	const want = "pair:\n  - 1\n  - 2\nlevels:\n  level-3: level-4\nppair:\n  - 5\n  - 6\n"
	if string(out) != want {
		t.Fatalf("Marshal = %q, want %q", out, want)
	}
	var back holderIn
	if err := yaml.New().Unmarshal(out, &back); err != nil {
		t.Fatalf("Unmarshal error = %v", err)
	}
	if wantBack := (holderIn{Pair: pairIn{1, 2}, Levels: map[levelIn]levelIn{3: 4}, PPair: &pairIn{5, 6}}); !reflect.DeepEqual(back, wantBack) {
		t.Errorf("round trip = %#v, want %#v", back, wantBack)
	}
	type tc struct {
		target any
		name   string
		doc    string
		reason string
	}
	tests := []tc{
		{name: "the hook refuses", doc: "pair: [1]\n", target: &holderIn{}, reason: "UNMARSHAL_FAILED"},
		{name: "a text hook refuses", doc: "levels: {level-x: level-1}\n", target: &holderIn{}, reason: "UNMARSHAL_FAILED"},
		{name: "a yaml.v3 node hook", doc: "a: 1\n", target: &foreign{}, reason: "UNMARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if err := yaml.New().Unmarshal([]byte(tc.doc), tc.target); !errs.HasReason(err, tc.reason) {
			t.Errorf("%s: Unmarshal error = %v, want %s", tc.name, err, tc.reason)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestUntaggedAndBareTaggedFields builds, at run time, a struct with a field
// that has no tag and one with the bare tag form yaml.v3 still reads — a
// form go vet rejects in source — and checks both keys.
func TestUntaggedAndBareTaggedFields(t *testing.T) {
	t.Parallel()
	structType := reflect.StructOf([]reflect.StructField{
		{Name: "MaxConns", Type: reflect.TypeFor[int]()},
		{Name: "Legacy", Type: reflect.TypeFor[int](), Tag: reflect.StructTag("legacy_key")},
	})
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"lower-cased name and bare tag", "maxconns: 7\nlegacy_key: 5\n"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		value := reflect.New(structType).Elem()
		value.Field(0).SetInt(7)
		value.Field(1).SetInt(5)
		out, err := yaml.New().Marshal(value.Interface())
		if err != nil {
			t.Fatalf("%s: Marshal error = %v", tc.name, err)
		}
		if string(out) != tc.want {
			t.Fatalf("%s: Marshal = %q, want %q", tc.name, out, tc.want)
		}
		back := reflect.New(structType)
		if err := yaml.New().Unmarshal(out, back.Interface()); err != nil {
			t.Fatalf("%s: Unmarshal error = %v", tc.name, err)
		}
		if back.Elem().Field(0).Int() != 7 || back.Elem().Field(1).Int() != 5 {
			t.Errorf("%s: Unmarshal = %#v", tc.name, back.Elem().Interface())
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// cyclic is a value that refers to itself.
type cyclic struct {
	// Next points back.
	Next *cyclic `yaml:"next"`
}

// TestMarshalRefuses checks the values the encoder refuses — none of them
// panics, each is a MarshalFailed.
func TestMarshalRefuses(t *testing.T) {
	t.Parallel()
	loop := &cyclic{}
	loop.Next = loop
	var self any
	self = &self
	type tc struct {
		value any
		name  string
	}
	tests := []tc{
		{name: "channel", value: make(chan int)},
		{name: "function", value: func() {}},
		{name: "complex", value: complex(1, 2)},
		{name: "invalid UTF-8", value: "\xff"},
		{name: "invalid UTF-8 key", value: map[string]int{"\xff": 1}},
		{name: "struct key", value: map[inlineBase]int{{}: 1}},
		{name: "array key", value: map[[2]int]int{{1, 2}: 1}},
		{name: "cycle of structs", value: loop},
		{name: "cycle of pointers", value: self},
		{name: "key past 1024 bytes", value: map[string]int{strings.Repeat("k", 1025): 1}},
		{name: "bad tag flag", value: struct {
			A int `yaml:"a,sideways"`
		}{}},
		{name: "duplicate tag key", value: struct {
			A int `yaml:"x"`
			B int `yaml:"x"`
		}{}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := yaml.New().Marshal(tc.value)
		if !errs.HasReason(err, "MARSHAL_FAILED") {
			t.Errorf("%s: Marshal error = %v, want MARSHAL_FAILED", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
