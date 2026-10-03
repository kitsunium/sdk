package yaml_test

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	goyaml "gopkg.in/yaml.v3"

	native "github.com/kitsunium/sdk/internal/service/codec/yaml"
)

// differentialSeeds are documents inside the native subset — the shapes a
// configuration file takes — plus a few the subset refuses, so the mutator
// starts from both sides of the line.
var differentialSeeds = []string{
	"a: 1\nb: two\nc: [1, 2, {x: y}]\n",
	"key:\n- a\n- b\nother: c\n",
	"- name: x\n  value: y\n- name: z\n",
	"- - a\n  - b\n- c\n",
	"text: |\n  line1\n  line2\nnext: x\n",
	"text: >\n  folded\n  line\n\n  para\nnext: x\n",
	"text: |-\n  strip\n",
	"text: |+\n  keep\n\nnext: x\n",
	"text: >2-\n   indented\n  less\n",
	"plain: this is\n  multi line\n\n  with para\nz: 1\n",
	"q: \"esc\\t\\u00e9\\x41\\\n  joined\"\n",
	"s: 'it''s'\nt: 'two\n\n  lines'\n",
	"n: ~\nm: null\ne:\nf: 1.5\ng: .inf\nh: -.inf\nj: 0x1F\nk: 0o17\nl: true\n",
	"---\na: 1\n...\n",
	"[1, 2, 3]",
	"{a: 1, b: [x, y], c: {d: e}}",
	"just a scalar",
	"|\n  root literal\n",
	"a:\n  b:\n    c: deep\n",
	"url: http://example.com:8080/path\n",
	"k: v # comment\n# full comment\nk2: v2\n",
	"empty_map: {}\nempty_list: []\n",
	"[a: 1, b]",
	"{a, b: 2}",
	"{\"json\": \"style\", \"n\": 1}",
	"key: \"multi\n  line quoted\"\n",
	"- |\n  in seq\n- x\n",
	"? complex\n: x\n",
	"&a x: 1\n",
	"x: !!str 1\n",
	"a: 1\n---\nb: 2\n",
	"perm: 0644\n",
}

// FuzzNativeAgreesWithYAMLv3 holds the native subset to one property: every
// document it accepts, gopkg.in/yaml.v3 accepts too and reads as the same
// value. The subset is meant to be a strict subset of YAML; a document the
// native codec reads differently from the reference reader is a bug in one of
// them, and a document only the native codec accepts is a leniency the subset
// never promised.
//
// Three differences are the subset's own and are skipped, not failed: yaml.v3
// reads a timestamp into time.Time and a mapping with a non-string key into
// map[any]any, where the subset keeps text; and yaml.v3 reads YAML 1.1
// numbers (1_000, 0b101, +0x1F, 0X1F) the YAML 1.2 core schema reads as text.
func FuzzNativeAgreesWithYAMLv3(f *testing.F) {
	//: the seeds.
	for _, seed := range differentialSeeds {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var got any
		//: a document the subset refuses proves nothing here.
		if err := native.New().Unmarshal(data, &got); err != nil {
			return
		}
		var want any
		//: the reference reader must accept what the subset accepts.
		if err := goyaml.Unmarshal(data, &want); err != nil {
			t.Fatalf("the native codec accepts %q, yaml.v3 refuses it: %v", data, err)
		}
		//: a multi-document input: yaml.v3 read the first document only, and
		//: the subset refused it — unreachable here, kept as a guard.
		if !comparable(want) {
			return
		}
		//: the same value.
		if !sameValue(got, want) {
			t.Fatalf("the native codec reads %q as %#v, yaml.v3 as %#v", data, got, want)
		}
	})
}

// comparable reports whether yaml.v3's reading of a document holds none of
// the shapes the subset reads differently by design: a time, or a mapping
// keyed by something other than a string.
func comparable(v any) bool {
	switch v := v.(type) {
	//: a timestamp, or a non-string key.
	case time.Time, map[any]any:
		return false
	//: a mapping.
	case map[string]any:
		for _, item := range v {
			if !comparable(item) {
				return false
			}
		}
	//: a sequence.
	case []any:
		for _, item := range v {
			if !comparable(item) {
				return false
			}
		}
	}
	return true
}

// sameValue compares the native reading with yaml.v3's, NaN equal to NaN,
// and a YAML 1.1 number yaml.v3 read as a number equal to the text the core
// schema keeps.
func sameValue(got, want any) bool {
	switch w := want.(type) {
	//: a mapping.
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for k, wv := range w {
			gv, present := g[k]
			if !present || !sameValue(gv, wv) {
				return false
			}
		}
		return true
	//: a sequence.
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !sameValue(g[i], w[i]) {
				return false
			}
		}
		return true
	//: a float.
	case float64:
		if g, ok := got.(float64); ok {
			return g == w || (math.IsNaN(g) && math.IsNaN(w))
		}
		text, isText := got.(string)
		return isText && isYAML11NumberText(text)
	//: an integer yaml.v3 read from a YAML 1.1 spelling.
	case int, int64, uint64:
		if reflect.DeepEqual(got, want) {
			return true
		}
		text, isText := got.(string)
		return isText && isYAML11NumberText(text)
	}
	//: a string, a boolean, null.
	return reflect.DeepEqual(got, want)
}

// isYAML11NumberText reports whether the native reading is text that YAML
// 1.1 (and yaml.v3) reads as a number: underscores, base 2, an uppercase
// prefix or a signed prefix the core schema does not have.
func isYAML11NumberText(text string) bool {
	lower := strings.ToLower(strings.TrimLeft(text, "+-"))
	return strings.Contains(text, "_") || strings.HasPrefix(lower, "0b") ||
		strings.HasPrefix(lower, "0x") || strings.HasPrefix(lower, "0o") || strings.HasPrefix(text, "0X")
}

// FuzzNativeOutputReadsTheSame holds the native encoder to the promise its
// quoting rules make: what it writes reads back as the same value in the
// native decoder AND in gopkg.in/yaml.v3 — a YAML 1.1 reader that takes yes,
// 0777, 1_000 and 2024-01-15 for something other than text. The values come
// from documents the native decoder accepts, so they span every shape the
// subset reads.
func FuzzNativeOutputReadsTheSame(f *testing.F) {
	//: the seeds.
	for _, seed := range differentialSeeds {
		f.Add([]byte(seed))
	}
	//: strings that only quoting keeps strings.
	f.Add([]byte(`[yes, "0777", "1_000", "2024-01-15", "~", "", " lead", "trail ", "a: b", "a #b", "-", "?x", ":x", "@x", "%x", "!x", "&x", "*x", "multi\nline", "\ttab", "--- x", "... x"]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var value any
		//: a document the subset refuses has no value to write.
		if err := native.New().Unmarshal(data, &value); err != nil {
			return
		}
		out, err := native.New().Marshal(value)
		//: every value the decoder produces can be written.
		if err != nil {
			t.Fatalf("Marshal(%#v) from %q: %v", value, data, err)
		}
		var again any
		//: the native decoder reads its own output.
		if err := native.New().Unmarshal(out, &again); err != nil {
			t.Fatalf("the native codec refuses its own output %q (from %q): %v", out, data, err)
		}
		//: as the same value.
		if !sameValue(again, value) {
			t.Fatalf("round trip of %q through %q gives %#v, want %#v", data, out, again, value)
		}
		var reference any
		//: yaml.v3 reads it.
		if err := goyaml.Unmarshal(out, &reference); err != nil {
			t.Fatalf("yaml.v3 refuses the native output %q (from %q): %v", out, data, err)
		}
		//: as the same value, strictly: no YAML 1.1 reading allowed here.
		if !reflect.DeepEqual(normalise(reference), normalise(value)) {
			t.Fatalf("yaml.v3 reads the native output %q as %#v, the native codec wrote %#v", out, reference, value)
		}
	})
}

// normalise makes two decoded values comparable with reflect.DeepEqual: NaN
// becomes a marker string, since NaN equals nothing.
func normalise(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			out[k] = normalise(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = normalise(item)
		}
		return out
	case float64:
		if math.IsNaN(v) {
			return "NaN"
		}
	}
	return v
}
