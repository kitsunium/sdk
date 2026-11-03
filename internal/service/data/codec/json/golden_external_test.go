package json_test

import (
	"bytes"
	stdjson "encoding/json"
	"flag"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/service/data/codec/json"
)

// updateGolden rewrites testdata/golden from the codec under test. It is how
// the files were written — on the base, before #893 moved the codec onto
// encoding/json/v2 — and must not be run to make a failing gate pass.
var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/golden from the codec under test")

// goldenInner is a nested record.
type goldenInner struct {
	Name  string            `json:"name"`
	Score float64           `json:"score"`
	Tags  []string          `json:"tags"`
	Extra map[string]string `json:"extra,omitempty"`
}

// goldenEmbedded is promoted into goldenRecord.
type goldenEmbedded struct {
	Promoted string `json:"promoted"`
	Shadowed string `json:"shadowed"`
}

// goldenText marshals itself as text: a map key and a value.
type goldenText struct{ v string }

// MarshalText writes the value upper-cased with a marker.
func (g goldenText) MarshalText() ([]byte, error) { return []byte("T:" + g.v), nil }

// UnmarshalText reads what MarshalText writes.
func (g *goldenText) UnmarshalText(b []byte) error {
	g.v = string(bytes.TrimPrefix(b, []byte("T:")))
	return nil
}

// goldenPtrJSON marshals itself only through its pointer: legacy semantics
// call it on an addressable value only.
type goldenPtrJSON struct{ N int }

// MarshalJSON writes a marker.
func (g *goldenPtrJSON) MarshalJSON() ([]byte, error) { return []byte(`"PTR"`), nil }

// goldenRecord holds every member kind whose spelling encoding/json's v1
// semantics decide.
type goldenRecord struct {
	goldenEmbedded
	Shadowed     int                    `json:"shadowed"`
	Bool         bool                   `json:"bool"`
	Int8         int8                   `json:"int8"`
	Uint64       uint64                 `json:"uint64"`
	MaxInt       int64                  `json:"max_int"`
	Float32      float32                `json:"float32"`
	Float64      float64                `json:"float64"`
	Floats       []float64              `json:"floats"`
	HTML         string                 `json:"html"`
	Separators   string                 `json:"separators"`
	Invalid      string                 `json:"invalid"`
	Bytes        []byte                 `json:"bytes"`
	ByteArray    [4]byte                `json:"byte_array"`
	NilSlice     []int                  `json:"nil_slice"`
	EmptySlice   []int                  `json:"empty_slice"`
	NilMap       map[string]int         `json:"nil_map"`
	IntKeys      map[int]string         `json:"int_keys"`
	TextKeys     map[goldenText]int     `json:"text_keys"`
	Text         goldenText             `json:"text"`
	OmitZero     int                    `json:"omit_zero,omitempty"`
	OmitFalse    bool                   `json:"omit_false,omitempty"`
	OmitNil      *int                   `json:"omit_nil,omitempty"`
	OmitEmptyMap map[string]int         `json:"omit_empty_map,omitempty"`
	KeepStruct   goldenInner            `json:"keep_struct,omitempty"`
	QuotedInt    int                    `json:"quoted_int,string"`
	QuotedBool   bool                   `json:"quoted_bool,string"`
	QuotedStr    string                 `json:"quoted_str,string"`
	When         time.Time              `json:"when"`
	Wait         time.Duration          `json:"wait"`
	Number       stdjson.Number         `json:"number"`
	Raw          stdjson.RawMessage     `json:"raw"`
	Any          any                    `json:"any"`
	Inner        *goldenInner           `json:"inner"`
	Children     []goldenInner          `json:"children"`
	ByName       map[string]goldenInner `json:"by_name"`
	PtrValue     goldenPtrJSON          `json:"ptr_value"`
	PtrPointer   *goldenPtrJSON         `json:"ptr_pointer"`
	Untagged     string
	Skipped      string `json:"-"`
	Dash         string `json:"-,"`
	unexported   string
	Interface    corecodec.Format          `json:"interface"`
	Nested       map[string]map[string]int `json:"nested"`
}

// goldenSample is the record every member set.
func goldenSample() goldenRecord {
	seven := 7
	return goldenRecord{
		goldenEmbedded: goldenEmbedded{Promoted: "up", Shadowed: "hidden by the outer field"},
		Shadowed:       9,
		Bool:           true, Int8: -8, Uint64: math.MaxUint64, MaxInt: math.MaxInt64,
		Float32: 3.1415927, Float64: 2.718281828459045,
		Floats:     []float64{0, math.Copysign(0, -1), 0.1, 1e20, 1e21, 1e-6, 1e-7, -1.5e-300, 123456789.125, float64(float32(0.1))},
		HTML:       "<a href=\"x\">&amp;</a>",
		Separators: "line para tab\t\x01",
		Invalid:    "bad \xff\xfe utf-8",
		Bytes:      []byte{0, 1, 2, 0xfe, 0xff},
		ByteArray:  [4]byte{1, 2, 3, 4},
		EmptySlice: []int{},
		IntKeys:    map[int]string{10: "ten", -1: "minus", 2: "two"},
		TextKeys:   map[goldenText]int{{"b"}: 2, {"a"}: 1},
		Text:       goldenText{"value"},
		OmitNil:    &seven,
		QuotedInt:  42, QuotedBool: true, QuotedStr: "q\"s",
		When:       time.Date(2026, 11, 3, 12, 30, 45, 123456789, time.FixedZone("CET", 3600)),
		Wait:       90 * time.Minute,
		Number:     "12.50",
		Raw:        stdjson.RawMessage(`{"pre":"encoded", "spaced" : [1, 2]}`),
		Any:        []any{1, "two", map[string]any{"z": nil, "a": 1.5}},
		Inner:      &goldenInner{Name: "in", Score: 1.25, Tags: []string{"x"}},
		Children:   []goldenInner{{Name: "c1"}, {Name: "c2", Extra: map[string]string{"k": "v"}}},
		ByName:     map[string]goldenInner{"b": {Name: "b"}, "a": {Name: "a"}},
		PtrValue:   goldenPtrJSON{1},
		PtrPointer: &goldenPtrJSON{2},
		Untagged:   "u", Skipped: "never", Dash: "dash", unexported: "never",
		Interface: "json",
		Nested:    map[string]map[string]int{"y": {"b": 2, "a": 1}, "x": nil},
	}
}

// goldenValues are the values whose encoding is pinned, by golden file name.
func goldenValues() map[string]any {
	rec := goldenSample()
	return map[string]any{
		"record":         rec,
		"record-pointer": &rec,
		"zero-record":    goldenRecord{},
		"slice-of-ptr":   []*goldenPtrJSON{{1}, nil},
		"array-of-ptr":   [2]goldenPtrJSON{{1}, {2}},
		"map-any":        map[string]any{"html": "<&>", "n": 1e21, "nil": nil, "nested": []any{[]any{}, map[string]any{}}},
		"string":         "plain \"quoted\" \\ <tag> é \U0001F338",
		"nil":            nil,
		"raw-message":    stdjson.RawMessage(`[1,2]`),
		"empty-raw":      stdjson.RawMessage(nil),
		"float-max":      math.MaxFloat64,
		"bytes":          []byte("binary\x00"),
	}
}

// goldenDecodes are inputs Unmarshal reads under v1 semantics — case-folded
// member names, the last of duplicate names, unknown members ignored,
// quoted numbers — each re-encoded to pin what was read.
func goldenDecodes() map[string]struct {
	input  string
	target func() any
} {
	return map[string]struct {
		input  string
		target func() any
	}{
		"decode-case-fold":  {`{"NAME":"upper","Score":1,"tags":null}`, func() any { return new(goldenInner) }},
		"decode-duplicates": {`{"name":"first","name":"second"}`, func() any { return new(goldenInner) }},
		"decode-unknown":    {`{"name":"n","unknown":{"deep":[1,2]},"extra":{"k":"v"}}`, func() any { return new(goldenInner) }},
		"decode-quoted":     {`{"quoted_int":"17","quoted_bool":"false","quoted_str":"\"x\""}`, func() any { return new(goldenRecord) }},
		"decode-any":        {`{"a":[1,2.5,"s",true,null,{"b":{}}],"n":1e300,"neg":-0,"big":12345678901234567890}`, func() any { var v any; return &v }},
		"decode-numbers":    {`{"n":12345678901234567890}`, func() any { return new(map[string]stdjson.Number) }},
		"decode-record":     {"", func() any { var v any; return &v }},
	}
}

// goldenPath is a golden file's path.
func goldenPath(name string) string {
	return filepath.Join("testdata", "golden", name+".json")
}

// checkGolden compares got with the golden file, or writes it under
// -update-golden.
func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := goldenPath(name)
	if *updateGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: the codec wrote\n%s\nencoding/json v1 wrote\n%s", name, got, want)
	}
}

// TestMarshalIsGolden is #893's byte-identity gate: every value above
// encodes — through Marshal, Append and the stream encoder — to the bytes the
// codec wrote on encoding/json v1 before it moved onto encoding/json/v2.
func TestMarshalIsGolden(t *testing.T) {
	c := json.New()
	a, _ := c.(corecodec.Appender)
	sc, _ := c.(corecodec.StreamingCodec)
	for name, v := range goldenValues() {
		got, err := c.Marshal(v)
		if err != nil {
			t.Fatalf("%s: Marshal: %v", name, err)
		}
		checkGolden(t, name, got)
		if appended, aerr := a.Append([]byte("PRE"), v); aerr != nil || !bytes.Equal(appended[3:], got) {
			t.Fatalf("%s: Append wrote %q, %v", name, appended, aerr)
		}
		var buf bytes.Buffer
		if eerr := sc.NewEncoder(&buf).Encode(v); eerr != nil || !bytes.Equal(buf.Bytes(), append(got, '\n')) {
			t.Fatalf("%s: the stream encoder wrote %q, %v", name, buf.Bytes(), eerr)
		}
	}
}

// TestUnmarshalIsGolden pins what Unmarshal and the stream decoder read under
// the v1 semantics: each input is decoded, then re-encoded, against the bytes
// the codec produced on encoding/json v1. "decode-record" reads back the
// record's own golden encoding.
func TestUnmarshalIsGolden(t *testing.T) {
	c := json.New()
	sc, _ := c.(corecodec.StreamingCodec)
	for name, tc := range goldenDecodes() {
		input := []byte(tc.input)
		if name == "decode-record" {
			var err error
			if input, err = os.ReadFile(goldenPath("record")); err != nil {
				t.Fatal(err)
			}
		}
		v := tc.target()
		if err := c.Unmarshal(input, v); err != nil {
			t.Fatalf("%s: Unmarshal: %v", name, err)
		}
		got, err := c.Marshal(v)
		if err != nil {
			t.Fatalf("%s: re-Marshal: %v", name, err)
		}
		checkGolden(t, name, got)
		streamed := tc.target()
		if derr := sc.NewDecoder(bytes.NewReader(input)).Decode(streamed); derr != nil {
			t.Fatalf("%s: stream Decode: %v", name, derr)
		}
		if again, _ := c.Marshal(streamed); !bytes.Equal(again, got) {
			t.Fatalf("%s: the stream decoder read %s, Unmarshal %s", name, again, got)
		}
	}
}
