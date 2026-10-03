package yaml_test

import (
	"math"
	"reflect"
	"testing"

	"github.com/kitsunium/sdk/internal/service/data/codec/yaml"
)

// TestTheSubsetReads decodes one document per construct the subset names as
// supported, into an untyped value, and compares it with what YAML 1.2.2 says
// the document means.
func TestTheSubsetReads(t *testing.T) {
	t.Parallel()
	type tc struct {
		want any
		name string
		doc  string
	}
	tests := []tc{
		//: block collections.
		{name: "block mapping", doc: "a: 1\nb: two\n", want: map[string]any{"a": 1, "b": "two"}},
		{name: "nested block mapping", doc: "a:\n  b:\n    c: deep\n", want: map[string]any{"a": map[string]any{"b": map[string]any{"c": "deep"}}}},
		{name: "indented root mapping", doc: "  a: 1\n  b: 2\n", want: map[string]any{"a": 1, "b": 2}},
		{name: "block sequence", doc: "- a\n- b\n", want: []any{"a", "b"}},
		{name: "sequence at its key's indentation", doc: "key:\n- a\n- b\nother: c\n", want: map[string]any{"key": []any{"a", "b"}, "other": "c"}},
		{name: "indented sequence", doc: "key:\n  - a\n  - b\n", want: map[string]any{"key": []any{"a", "b"}}},
		{name: "compact mapping in a sequence", doc: "- name: x\n  value: y\n- name: z\n", want: []any{map[string]any{"name": "x", "value": "y"}, map[string]any{"name": "z"}}},
		{name: "compact sequence in a sequence", doc: "- - a\n  - b\n- c\n", want: []any{[]any{"a", "b"}, "c"}},
		{name: "empty values", doc: "a:\nb:\n- \n-\n", want: nil},
		{name: "empty entries", doc: "-\n- \n", want: []any{nil, nil}},
		//: flow collections.
		{name: "flow sequence", doc: "[1, 2, 3]", want: []any{1, 2, 3}},
		{name: "flow mapping", doc: "{a: 1, b: [x, y], c: {d: e}}", want: map[string]any{"a": 1, "b": []any{"x", "y"}, "c": map[string]any{"d": "e"}}},
		{name: "flow across lines", doc: "a: [1,\n  2,\n  3,\n]\n", want: map[string]any{"a": []any{1, 2, 3}}},
		{name: "flow single pair", doc: "[a: 1, b]", want: []any{map[string]any{"a": 1}, "b"}},
		{name: "flow key without value", doc: "{a, b: 2}", want: map[string]any{"a": nil, "b": 2}},
		{name: "JSON-like flow", doc: `{"json":"style", "n": 1}`, want: map[string]any{"json": "style", "n": 1}},
		{name: "empty flow collections", doc: "m: {}\nl: []\n", want: map[string]any{"m": map[string]any{}, "l": []any{}}},
		{name: "colon inside a flow scalar", doc: "[a:b, http://x]", want: []any{"a:b", "http://x"}},
		//: plain scalars.
		{name: "plain with colon and hash", doc: "url: http://example.com:8080/p#x\n", want: map[string]any{"url": "http://example.com:8080/p#x"}},
		{name: "multi-line plain folds", doc: "p: this is\n  one line\n\n  and a paragraph\n", want: map[string]any{"p": "this is one line\nand a paragraph"}},
		{name: "plain starting with an indicator and a letter", doc: "a: -x\nb: ?y\nc: :z\n", want: map[string]any{"a": "-x", "b": "?y", "c": ":z"}},
		//: quoted scalars.
		{name: "single-quoted", doc: "s: 'it''s # not a comment'\n", want: map[string]any{"s": "it's # not a comment"}},
		{name: "double-quoted escapes", doc: `q: "tab\there \u00e9\x41 \U0001F338 \N\_\L\P \0 \"q\" \\"` + "\n", want: map[string]any{"q": "tab\there éA 🌸 \u0085\u00a0\u2028\u2029 \x00 \"q\" \\"}},
		{name: "double-quoted escaped line break", doc: "q: \"one\\\n  two\"\n", want: map[string]any{"q": "onetwo"}},
		{name: "quoted folding", doc: "q: \"one\n  two\n\n  three\"\n", want: map[string]any{"q": "one two\nthree"}},
		{name: "quoted keys", doc: "\"a b\": 1\n'c': 2\n", want: map[string]any{"a b": 1, "c": 2}},
		//: block scalars.
		{name: "literal clip", doc: "t: |\n  line1\n  line2\nn: x\n", want: map[string]any{"t": "line1\nline2\n", "n": "x"}},
		{name: "literal strip", doc: "t: |-\n  strip\n", want: map[string]any{"t": "strip"}},
		{name: "literal keep", doc: "t: |+\n  keep\n\nn: x\n", want: map[string]any{"t": "keep\n\n", "n": "x"}},
		{name: "literal more indented lines", doc: "t: |\n  a\n    b\n  c\n", want: map[string]any{"t": "a\n  b\nc\n"}},
		{name: "literal indentation indicator", doc: "t: |2-\n   lead\n  next\n", want: map[string]any{"t": " lead\nnext"}},
		{name: "literal leading empty line", doc: "t: |\n\n  text\n", want: map[string]any{"t": "\ntext\n"}},
		{name: "folded", doc: "t: >\n  folded\n  line\n\n  para\n", want: map[string]any{"t": "folded line\npara\n"}},
		{name: "folded more indented", doc: "t: >-\n  a\n    b\n  c\n", want: map[string]any{"t": "a\n  b\nc"}},
		{name: "block scalar in a sequence", doc: "- |\n  in seq\n- x\n", want: []any{"in seq\n", "x"}},
		{name: "root literal", doc: "|\n  root\n", want: "root\n"},
		//: comments and documents.
		{name: "comments", doc: "# head\nk: v # tail\n  # indented\nk2: v2\n", want: map[string]any{"k": "v", "k2": "v2"}},
		{name: "document markers", doc: "---\na: 1\n...\n# after\n", want: map[string]any{"a": 1}},
		{name: "explicit empty document", doc: "---\n", want: nil},
		{name: "byte order mark", doc: "\ufeffa: 1\n", want: map[string]any{"a": 1}},
		{name: "carriage returns", doc: "a: 1\r\nb: |\r\n  x\r\n", want: map[string]any{"a": 1, "b": "x\n"}},
		{name: "tab after a key", doc: "a:\tb\n", want: map[string]any{"a": "b"}},
		//: the core schema.
		{name: "nulls", doc: "[~, null, Null, NULL, ]", want: []any{nil, nil, nil, nil}},
		{name: "booleans", doc: "[true, True, TRUE, false, False, FALSE]", want: []any{true, true, true, false, false, false}},
		{name: "YAML 1.1 booleans are text", doc: "[yes, no, on, off, y, n]", want: []any{"yes", "no", "on", "off", "y", "n"}},
		{name: "integers", doc: "[0, -1, +7, 0o17, 0x1F, 9223372036854775807, 18446744073709551615]", want: []any{0, -1, 7, 15, 31, math.MaxInt, maxUint64}},
		{name: "floats", doc: "[1.5, -0.5, .5, 1., 1e3, 2.5E-2, .inf, -.Inf, +.INF]", want: []any{1.5, -0.5, 0.5, 1.0, 1000.0, 0.025, math.Inf(1), math.Inf(-1), math.Inf(1)}},
		{name: "numbers in other dialects are text", doc: "[1_000, 0b101, 0X1F, -0x1F, 1:30, 2024-01-15]", want: []any{"1_000", "0b101", "0X1F", "-0x1F", "1:30", "2024-01-15"}},
		{name: "quoted numbers are text", doc: `["1", '2', "true", "null"]`, want: []any{"1", "2", "true", "null"}},
		{name: "keys are their text", doc: "1: a\ntrue: b\n~: c\n", want: map[string]any{"1": "a", "true": "b", "~": "c"}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var got any
		//: the document decodes.
		if err := yaml.New().Unmarshal([]byte(tc.doc), &got); err != nil {
			t.Fatalf("%s: Unmarshal(%q) error = %v", tc.name, tc.doc, err)
		}
		//: "empty values" holds nulls only: compare its shape by hand.
		if tc.name == "empty values" {
			want := map[string]any{"a": nil, "b": []any{nil, nil}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s: got %#v, want %#v", tc.name, got, want)
			}
			return
		}
		//: the value YAML 1.2.2 gives it.
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: Unmarshal(%q) = %#v, want %#v", tc.name, tc.doc, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestNaNIsAFloat decodes .nan, which reflect.DeepEqual cannot compare.
func TestNaNIsAFloat(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		doc  string
	}
	tests := []tc{{"lower", ".nan"}, {"title", ".NaN"}, {"upper", ".NAN"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var got any
		if err := yaml.New().Unmarshal([]byte(tc.doc), &got); err != nil {
			t.Fatalf("%s: Unmarshal error = %v", tc.name, err)
		}
		if f, ok := got.(float64); !ok || !math.IsNaN(f) {
			t.Errorf("%s: Unmarshal(%q) = %#v, want NaN", tc.name, tc.doc, got)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestAnEmptyDocumentLeavesTheTargetAlone decodes documents that hold no
// node: the target keeps its value, as it did under gopkg.in/yaml.v3.
func TestAnEmptyDocumentLeavesTheTargetAlone(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		doc  string
	}
	tests := []tc{{"nothing", ""}, {"blank lines", "\n\n  \n"}, {"comments only", "# a\n  # b\n"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		target := map[string]any{"kept": true}
		if err := yaml.New().Unmarshal([]byte(tc.doc), &target); err != nil {
			t.Fatalf("%s: Unmarshal error = %v", tc.name, err)
		}
		if !reflect.DeepEqual(target, map[string]any{"kept": true}) {
			t.Errorf("%s: target = %#v, want it untouched", tc.name, target)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
