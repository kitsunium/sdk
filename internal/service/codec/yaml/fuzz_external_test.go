package yaml_test

import (
	"bytes"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/yaml"
)

// fuzzTarget is the typed target the fuzzer decodes into, so a mutated
// document reaches the reflection decoder and not only the untyped one.
type fuzzTarget struct {
	// Rest catches every key no field declares.
	Rest map[string]any `yaml:",inline"`
	// Nested is a nested struct.
	Nested *fuzzNested `yaml:"nested"`
	// Name is a string.
	Name string `yaml:"name"`
	// List is a sequence of strings.
	List []string `yaml:"list"`
	// Counts is a map of integers.
	Counts map[string]int `yaml:"counts"`
	// Keys is a map with integer keys.
	Keys map[int]bool `yaml:"keys"`
	// Port is an integer.
	Port int `yaml:"port"`
	// Ratio is a float.
	Ratio float32 `yaml:"ratio"`
	// Wait is a duration.
	Wait time.Duration `yaml:"wait"`
	// Flag is a boolean.
	Flag bool `yaml:"flag"`
}

// fuzzNested is fuzzTarget's nested struct.
type fuzzNested struct {
	// When is a time.
	When time.Time `yaml:"when"`
	// Pair is a fixed-size array.
	Pair [2]uint8 `yaml:"pair"`
}

// fuzzSeeds is the corpus the mutator starts from: every construct of the
// subset, every refused one, and the shapes that probe the bounds.
func fuzzSeeds() []string {
	//: valid documents, refusals, and degenerate inputs.
	return []string{
		"name: kit\nport: 8080\nflag: true\nratio: 0.5\nwait: 30s\n",
		"list: [a, b, \"c d\"]\ncounts: {x: 1, y: 2}\nkeys:\n  1: true\n  0x2: false\n",
		"nested:\n  when: 2024-01-15T09:30:00Z\n  pair: [1, 2]\nextra: [1, {a: b}]\n",
		"- name: x\n  value: y\n- - a\n  - b\n",
		"text: |+\n  keep\n\nfold: >-\n  a\n  b\n\n  c\nlit: |2\n   x\n",
		"q: \"esc \\t \\u00e9 \\x41 \\U0001F338 \\\n  joined\"\ns: 'it''s'\n",
		"p: this is\n  multi line\n\n  plain\n",
		"---\na: 1\n...\n",
		"[1, -2, 0o17, 0x1F, 1.5, .inf, -.inf, .nan, ~, true, null, \"\"]",
		"{a: 1, b: [x, y], c: {d: e}, f}",
		"a: &x 1\nb: *x\n",
		"!!str x",
		"<<: {a: 1}\n",
		"? k\n: v\n",
		"%YAML 1.2\n---\na: 1\n",
		"a: 1\n---\nb: 2\n",
		"a: 1\na: 2\n",
		"m: 0644\n",
		"\ta: 1\n",
		"a: \"open\n",
		"[[[[[[[[[[[[[[[[x]]]]]]]]]]]]]]]]",
		"\ufeff# bom\r\na: 1\r\n",
		"",
	}
}

// FuzzUnmarshal holds the codec to four properties on any input: decoding
// never panics, into an untyped or a typed target; a document decoded into an
// untyped value is written back and read back as the same value; and the
// streaming decoder reads any input to its end or to a refusal.
func FuzzUnmarshal(f *testing.F) {
	//: the corpus.
	for _, seed := range fuzzSeeds() {
		f.Add([]byte(seed))
	}
	c := yaml.New()
	f.Fuzz(func(t *testing.T, data []byte) {
		var typed fuzzTarget
		//: a refusal is a value, never a panic — and always a decoding
		//: failure by code, whichever construct it names.
		if err := c.Unmarshal(data, &typed); err != nil && !errs.HasCode(err, yaml.CodeYAMLUnmarshalFailed) {
			t.Fatalf("decoding %q into a struct fails with %v, which is not an UnmarshalFailed", data, err)
		}
		var value any
		//: a document the subset refuses has no value to round-trip.
		if err := c.Unmarshal(data, &value); err != nil {
			checkStream(t, c, data)
			return
		}
		out, err := c.Marshal(value)
		//: every value the decoder produces can be written.
		if err != nil {
			t.Fatalf("Marshal(%#v) from %q: %v", value, data, err)
		}
		var again any
		//: and read back.
		if err := c.Unmarshal(out, &again); err != nil {
			t.Fatalf("Unmarshal(Marshal(%q)) = %q: %v", data, out, err)
		}
		//: as the same value.
		if !equalValues(again, value) {
			t.Fatalf("round trip of %q through %q gives %#v, want %#v", data, out, again, value)
		}
		checkStream(t, c, data)
	})
}

// checkStream reads data as a stream and requires it to end: io.EOF or a
// refusal, after finitely many documents.
func checkStream(t *testing.T, c codec.Codec, data []byte) {
	t.Helper()
	sc, ok := c.(codec.StreamingCodec)
	//: the codec streams.
	if !ok {
		t.Fatal("the YAML codec does not stream")
	}
	dec := sc.NewDecoder(bytes.NewReader(data))
	//: a document is at least a line: no more Decode calls than lines, plus one.
	for range strings.Count(string(data), "\n") + 2 {
		var doc any
		err := dec.Decode(&doc)
		//: the end, or a refusal.
		if err != nil {
			return
		}
	}
	//: more documents than the input has lines.
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Fatalf("the stream decoder over %q does not end: %v", data, err)
	}
}

// equalValues compares two decoded values, NaN equal to NaN.
func equalValues(a, b any) bool {
	switch av := a.(type) {
	//: a mapping.
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, item := range av {
			other, present := bv[k]
			if !present || !equalValues(item, other) {
				return false
			}
		}
		return true
	//: a sequence.
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !equalValues(av[i], bv[i]) {
				return false
			}
		}
		return true
	//: a float.
	case float64:
		bv, ok := b.(float64)
		return ok && (av == bv || (math.IsNaN(av) && math.IsNaN(bv)))
	}
	//: anything else.
	return reflect.DeepEqual(a, b)
}
