// Package toml_test — encoding: the layout, byte for byte the one the replaced
// library wrote, and the values refused rather than written wrong.
package toml_test

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	corecodec "github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/toml"
)

// goldenStruct is what github.com/pelletier/go-toml/v2 v2.4.3 wrote for
// goldenValue, byte for byte: the native encoder keeps the layout.
const goldenStruct = `# what this is
title = "it's \"golden\""
notes = """
line one
line two	tab"""
ports = [
  80,
  443
]
owner = {name = 'ada', port = 1}
# off = 9
ratio = 1.0
started = 2024-01-15T09:30:00Z
'quoted key' = 'q'
strings = ['plain', "it's", "tab\there", 'ünïcode']

[server]
name = 'srv'
port = 8080

[[replicas]]
name = 'r1'
port = 1

[[replicas]]
name = 'r2'
port = 2

[labels]
alpha = 'a'
'with space' = 's'
zeta = 'z'

[empty]
`

// goldenMap is what the replaced library wrote for goldenMapValue.
const goldenMap = `b = 1
c = []
d = 0.00000015

[a]
y = 's'

[[a.z]]
k = 1

[[a.z]]
k = 2
`

// goldenInner is a table of golden.
type goldenInner struct {
	Name string `toml:"name"`
	Port int    `toml:"port"`
}

// golden carries every struct tag option the encoder reads.
type golden struct {
	Title    string            `toml:"title" comment:"what this is"`
	Debug    bool              `toml:"debug,omitempty"`
	Hidden   string            `toml:"hidden,omitempty"`
	Retries  int               `toml:"retries,omitzero"`
	Notes    string            `toml:"notes,multiline"`
	Ports    []int             `toml:"ports,multiline"`
	Owner    goldenInner       `toml:"owner,inline"`
	Off      int               `toml:"off,commented"`
	Server   goldenInner       `toml:"server"`
	Replicas []goldenInner     `toml:"replicas"`
	Labels   map[string]string `toml:"labels"`
	Ratio    float64           `toml:"ratio"`
	Started  time.Time         `toml:"started"`
	Skipped  string            `toml:"-"`
	NilPtr   *goldenInner      `toml:"nil_ptr"`
	Empty    map[string]int    `toml:"empty"`
	Quoted   string            `toml:"quoted key"`
	Strings  []string          `toml:"strings"`
}

// goldenValue returns the struct goldenStruct spells.
func goldenValue() golden {
	return golden{
		Title:    "it's \"golden\"",
		Notes:    "line one\nline two\ttab",
		Ports:    []int{80, 443},
		Owner:    goldenInner{Name: "ada", Port: 1},
		Off:      9,
		Server:   goldenInner{Name: "srv", Port: 8080},
		Replicas: []goldenInner{{Name: "r1", Port: 1}, {Name: "r2", Port: 2}},
		Labels:   map[string]string{"zeta": "z", "alpha": "a", "with space": "s"},
		Ratio:    1,
		Started:  time.Date(2024, 1, 15, 9, 30, 0, 0, time.UTC),
		Empty:    map[string]int{},
		Quoted:   "q",
		Strings:  []string{"plain", "it's", "tab\there", "ünïcode"},
	}
}

// goldenMapValue returns the map goldenMap spells.
func goldenMapValue() map[string]any {
	return map[string]any{
		"b": 1,
		"a": map[string]any{"z": []any{map[string]any{"k": 1}, map[string]any{"k": 2}}, "y": "s"},
		"c": []any{},
		"d": 1.5e-7,
	}
}

// TestGoldenLayout pins the layout: plain keys before tables, a blank line
// before each header, struct fields in declaration order, map keys sorted,
// strings literal when they can be, and every tag option.
func TestGoldenLayout(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		value any
		want  string
	}
	tests := []tc{
		{"struct", goldenValue(), goldenStruct},
		{"pointer to struct", new(goldenValue()), goldenStruct},
		{"map", goldenMapValue(), goldenMap},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, err := toml.New().Marshal(tc.value)
		//: encodes.
		if err != nil {
			t.Fatalf("%s: Marshal error = %v %v", tc.name, err, errs.FieldsOf(err))
		}
		//: byte for byte.
		if string(got) != tc.want {
			t.Errorf("%s: encoded\n%s\nwant\n%s", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestGoldenRoundTrip decodes the golden documents back into their values.
func TestGoldenRoundTrip(t *testing.T) {
	t.Parallel()
	var got golden
	//: the struct document decodes.
	if err := toml.New().Unmarshal([]byte(goldenStruct), &got); err != nil {
		t.Fatal(err)
	}
	want := goldenValue()
	want.Off = 0
	want.Empty = map[string]int{}
	//: everything but the commented-out field, which is a comment now.
	if got.Title != want.Title || got.Notes != want.Notes || got.Off != 0 || len(got.Replicas) != 2 ||
		!got.Started.Equal(want.Started) || got.Labels["with space"] != "s" || got.Strings[3] != "ünïcode" {
		t.Errorf("round trip = %+v", got)
	}
}

// TestScalarSpellings pins how each scalar is written.
func TestScalarSpellings(t *testing.T) {
	t.Parallel()
	var largestInt int64 = math.MaxInt64
	var largestUint uint64 = math.MaxInt64
	type tc struct {
		name  string
		value any
		want  string
	}
	tests := []tc{
		{"integer float", 3.0, "v = 3.0\n"},
		{"float32", float32(3.1415927), "v = 3.1415927\n"},
		{"negative zero", math.Copysign(0, -1), "v = -0.0\n"},
		{"infinity", math.Inf(1), "v = inf\n"},
		{"negative infinity", math.Inf(-1), "v = -inf\n"},
		{"nan", math.NaN(), "v = nan\n"},
		{"largest int64", largestInt, "v = 9223372036854775807\n"},
		{"unsigned at the limit", largestUint, "v = 9223372036854775807\n"},
		{"bytes are integers", []byte{0, 255}, "v = [0, 255]\n"},
		{"literal string", "plain", "v = 'plain'\n"},
		{"apostrophe needs quotes", "it's", "v = \"it's\"\n"},
		{"control characters escaped", "a\x01\x7f\b\f\r\n\t\"\\", `v = "a\u0001\u007F\b\f\r\n\t\"\\"` + "\n"},
		{"local date", toml.LocalDate{Year: 2024, Month: 2, Day: 29}, "v = 2024-02-29\n"},
		{"local time", toml.LocalTime{Hour: 7, Minute: 32, Nanosecond: 500000000}, "v = 07:32:00.5\n"},
		{"local time precision", toml.LocalTime{Hour: 7, Minute: 32, Nanosecond: 500000000, Precision: 3}, "v = 07:32:00.500\n"},
		{"local date-time", toml.LocalDateTime{Year: 1979, Month: 5, Day: 27, Hour: 7}, "v = 1979-05-27T07:00:00\n"},
		{"offset date-time", time.Date(1979, 5, 27, 7, 32, 0, 999000000, time.FixedZone("", -7*3600)), "v = 1979-05-27T07:32:00.999-07:00\n"},
		{"nil pointer is its zero", (*int)(nil), "v = 0\n"},
		{"mixed array", []any{1, "a", []any{2}, map[string]any{"k": true}}, "v = [1, 'a', [2], {k = true}]\n"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, err := toml.New().Marshal(map[string]any{"v": tc.value})
		//: encodes.
		if err != nil {
			t.Fatalf("%s: %v %v", tc.name, err, errs.FieldsOf(err))
		}
		//: as spelled.
		if string(got) != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// multilineQuotes carries a string whose quote runs the multi-line form must
// escape.
type multilineQuotes struct {
	S string `toml:"s,multiline"`
}

// TestMultilineStringsRoundTrip writes awkward strings as multi-line strings
// and reads back the same value.
func TestMultilineStringsRoundTrip(t *testing.T) {
	t.Parallel()
	//: quotes next to the delimiter, runs of three and more, backslashes, CRLF.
	for _, s := range []string{"a\n\"", "a\n\"\"", "a\n\"\"\"b", "\"\"\"\"\"\"\n", "x\\\ny", "crlf\r\nline", "\n", "tab\tend\n"} {
		encoded, err := toml.New().Marshal(multilineQuotes{S: s})
		//: encodes.
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		var back multilineQuotes
		//: and reads back exactly.
		if err := toml.New().Unmarshal(encoded, &back); err != nil || back.S != s {
			t.Errorf("%q: encoded %q, decoded %q (%v)", s, encoded, back.S, err)
		}
	}
}

// cyclic refers to itself.
type cyclic struct {
	Next *cyclic `toml:"next"`
}

// textFails is a TextMarshaler that refuses.
type textFails struct{}

// errTextFails is textFails's refusal.
var errTextFails = errors.New("text refused")

// MarshalText refuses.
func (textFails) MarshalText() ([]byte, error) { return nil, errTextFails }

// clashKey writes every key as the same text.
type clashKey int

// MarshalText writes the same text for every key.
func (clashKey) MarshalText() ([]byte, error) { return []byte("same"), nil }

// TestMarshalRefusals pins every value the encoder refuses, as MARSHAL_FAILED
// naming the problem.
func TestMarshalRefusals(t *testing.T) {
	t.Parallel()
	loop := &cyclic{}
	loop.Next = loop
	type tc struct {
		name    string
		value   any
		problem string
	}
	tests := []tc{
		{"nil", nil, "a nil value has no TOML representation"},
		{"nil pointer root", (*golden)(nil), "a nil value has no TOML representation"},
		{"integer root", 1, "a TOML document is a table: the value must be a map or a struct"},
		{"slice root", []int{1}, "a TOML document is a table: the value must be a map or a struct"},
		{"time root", time.Now(), "a TOML document is a table: the value must be a map or a struct"},
		{"channel", map[string]any{"c": make(chan int)}, "the type has no TOML representation"},
		{"function", map[string]any{"f": func() {}}, "the type has no TOML representation"},
		{"complex", map[string]any{"c": complex(1, 2)}, "the type has no TOML representation"},
		{"nil interface in an array", map[string]any{"a": []any{nil}}, "a nil value has no TOML representation"},
		{"unsigned above int64", map[string]uint64{"u": math.MaxUint64}, "the unsigned integer is larger than a TOML integer can hold"},
		{"value not UTF-8", map[string]string{"k": "\xff"}, "a string is not valid UTF-8"},
		{"key not UTF-8", map[string]int{"\xff": 1}, "a string is not valid UTF-8"},
		{"array key", map[[2]int]int{{1, 2}: 3}, "the map's key type cannot be written as a TOML key"},
		{"two keys written alike", map[clashKey]int{1: 1, 2: 2}, "two map keys are written as the same TOML key"},
		{"cycle", loop, "the value is nested too deep, or refers to itself"},
		{"text refused", map[string]any{"t": textFails{}}, "a MarshalText method returned an error"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out, err := toml.New().Marshal(tc.value)
		//: refused, nothing returned.
		if !errs.HasReason(err, "MARSHAL_FAILED") || out != nil {
			t.Fatalf("%s: Marshal = %q, %v; want MARSHAL_FAILED", tc.name, out, err)
		}
		//: the problem named.
		if got := fieldOf(err, "problem"); got != tc.problem {
			t.Errorf("%s: problem = %q, want %q", tc.name, got, tc.problem)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestMarshalTextCauseKept pins that a MarshalText error stays reachable
// under the codec's refusal.
func TestMarshalTextCauseKept(t *testing.T) {
	t.Parallel()
	_, err := toml.New().Marshal(map[string]any{"t": textFails{}})
	//: the method's own error is in the chain.
	if !errors.Is(err, errTextFails) {
		t.Errorf("err = %v, want it to wrap the MarshalText error", err)
	}
}

// TestAppendLeavesDestinationOnFailure pins the Appender contract: on success
// the document follows the prefix; on failure the destination's length is
// unchanged.
func TestAppendLeavesDestinationOnFailure(t *testing.T) {
	t.Parallel()
	appender, ok := toml.New().(corecodec.Appender)
	//: the codec appends.
	if !ok {
		t.Fatal("the TOML codec does not implement codec.Appender")
	}
	dst := append(make([]byte, 0, 64), "# prefix\n"...)
	got, err := appender.Append(dst, map[string]int{"a": 1})
	//: the document after the prefix.
	if text := string(got); err != nil || text != "# prefix\na = 1\n" {
		t.Errorf("Append = %q, %v", text, err)
	}
	refused, err := appender.Append(dst, map[string]any{"a": 1, "c": make(chan int)})
	//: a refusal returns the destination as it was.
	if err == nil || !bytes.Equal(refused, dst) {
		t.Errorf("Append on failure = %q, %v", refused, err)
	}
}

// TestMarshalIsDeterministic encodes a wide map many times: the same bytes
// every time.
func TestMarshalIsDeterministic(t *testing.T) {
	t.Parallel()
	value := map[string]any{}
	//: enough keys for Go's map order to vary between iterations.
	for i := range 200 {
		value["key"+itoa(i)] = map[string]any{"n": i, "s": strings.Repeat("x", i%5)}
	}
	first, err := toml.New().Marshal(value)
	//: encodes.
	if err != nil {
		t.Fatal(err)
	}
	//: the same bytes on every run.
	for range 20 {
		again, err := toml.New().Marshal(value)
		//: identical.
		if err != nil || !bytes.Equal(first, again) {
			t.Fatalf("two encodes of one value differ: %v", err)
		}
	}
}

// failingWriter refuses every write.
type failingWriter struct{}

// errWriteFails is failingWriter's refusal.
var errWriteFails = errors.New("disk full")

// Write refuses.
func (failingWriter) Write([]byte) (int, error) { return 0, errWriteFails }

// TestStreamingEncoder writes one document per Encode, and surfaces a writer's
// refusal as MARSHAL_FAILED over the writer's error.
func TestStreamingEncoder(t *testing.T) {
	t.Parallel()
	sc, ok := toml.New().(corecodec.StreamingCodec)
	//: the codec streams.
	if !ok {
		t.Fatal("the TOML codec does not stream")
	}
	var buf bytes.Buffer
	enc := sc.NewEncoder(&buf)
	//: two documents, one after the other.
	if err := enc.Encode(map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(map[string]int{"b": 2}); err != nil {
		t.Fatal(err)
	}
	//: written as they come.
	if buf.String() != "a = 1\nb = 2\n" {
		t.Errorf("stream = %q", buf.String())
	}
	err := sc.NewEncoder(failingWriter{}).Encode(map[string]int{"a": 1})
	//: the writer's error, under MARSHAL_FAILED.
	if !errs.HasReason(err, "MARSHAL_FAILED") || !errors.Is(err, errWriteFails) {
		t.Errorf("err = %v", err)
	}
	//: a value refused before anything is written.
	if err := sc.NewEncoder(&buf).Encode(1); !errs.HasReason(err, "MARSHAL_FAILED") {
		t.Errorf("err = %v", err)
	}
}

// TestStreamingDecoderBound refuses a stream longer than the document cap, and
// reads one at the cap.
func TestStreamingDecoderBound(t *testing.T) {
	t.Parallel()
	sc, _ := toml.New().(corecodec.StreamingCodec)
	var out map[string]any
	err := sc.NewDecoder(bytes.NewReader(bytes.Repeat([]byte("\n"), 10<<20+1))).Decode(&out)
	//: past the cap.
	if fieldOf(err, "problem") != "the document is larger than the limit" {
		t.Errorf("err = %v %v", err, errs.FieldsOf(err))
	}
	dec := sc.NewDecoder(strings.NewReader("a = 1\n"))
	//: a document, then the end of the stream.
	if err := dec.Decode(&out); err != nil || out["a"] != int64(1) || dec.More() {
		t.Errorf("Decode = %v, out = %v, More = %v", err, out, dec.More())
	}
}

// failingReader fails with an error that is not an end of input.
type failingReader struct{}

// errReadFails is failingReader's error.
var errReadFails = errors.New("connection reset")

// Read fails.
func (failingReader) Read([]byte) (int, error) { return 0, errReadFails }

// TestStreamingDecoderReadError surfaces a reader's failure as
// UNMARSHAL_FAILED over the reader's error.
func TestStreamingDecoderReadError(t *testing.T) {
	t.Parallel()
	sc, _ := toml.New().(corecodec.StreamingCodec)
	var out map[string]any
	err := sc.NewDecoder(failingReader{}).Decode(&out)
	//: the reader's error, under UNMARSHAL_FAILED.
	if !errs.HasReason(err, "UNMARSHAL_FAILED") || !errors.Is(err, errReadFails) {
		t.Errorf("err = %v", err)
	}
}
