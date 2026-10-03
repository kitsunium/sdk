package yaml_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/yaml"
)

// position reads the line and column fields a refusal carries.
func position(err error) (line, column string) {
	//: every field.
	for _, field := range errs.FieldsOf(err) {
		switch field.Key() {
		case "line":
			line = field.StringValue()
		case "column":
			column = field.StringValue()
		}
	}
	return line, column
}

// TestEveryRefusedConstructIsRefusedByName decodes one document per construct
// the subset refuses and checks the refusal names it — by its own code — and
// where it starts, and that every refusal is also an UnmarshalFailed by code.
func TestEveryRefusedConstructIsRefusedByName(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		doc    string
		line   string
		column string
		code   errs.Code
	}
	tests := []tc{
		{name: "anchor", doc: "a: &base 1\n", code: yaml.CodeYAMLAnchorRefused, line: "1", column: "4"},
		{name: "anchor on a key", doc: "x: 1\n&k y: 2\n", code: yaml.CodeYAMLAnchorRefused, line: "2", column: "1"},
		{name: "alias", doc: "a: 1\nb: *base\n", code: yaml.CodeYAMLAliasRefused, line: "2", column: "4"},
		{name: "alias in flow", doc: "[a, *b]", code: yaml.CodeYAMLAliasRefused, line: "1", column: "5"},
		{name: "local tag", doc: "a: !thing 1\n", code: yaml.CodeYAMLTagRefused, line: "1", column: "4"},
		{name: "core tag", doc: "- !!str 1\n", code: yaml.CodeYAMLTagRefused, line: "1", column: "3"},
		{name: "verbatim tag", doc: "!<tag:x> a", code: yaml.CodeYAMLTagRefused, line: "1", column: "1"},
		{name: "merge key", doc: "base: {a: 1}\nderived:\n  <<: {b: 2}\n", code: yaml.CodeYAMLMergeKeyRefused, line: "3", column: "3"},
		{name: "merge key in flow", doc: "{<<: {a: 1}}", code: yaml.CodeYAMLMergeKeyRefused, line: "1", column: "2"},
		{name: "second document after ---", doc: "a: 1\n---\nb: 2\n", code: yaml.CodeYAMLMultiDocRefused, line: "2", column: "1"},
		{name: "second document after ...", doc: "a: 1\n...\nb: 2\n", code: yaml.CodeYAMLMultiDocRefused, line: "3", column: "1"},
		{name: "two explicit documents", doc: "---\n---\n", code: yaml.CodeYAMLMultiDocRefused, line: "2", column: "1"},
		{name: "complex key", doc: "? a\n: b\n", code: yaml.CodeYAMLComplexKeyRefused, line: "1", column: "1"},
		{name: "flow collection as a key", doc: "[a, b]: c\n", code: yaml.CodeYAMLComplexKeyRefused, line: "1", column: "1"},
		{name: "flow mapping key that is a collection", doc: "{[a]: b}", code: yaml.CodeYAMLComplexKeyRefused, line: "1", column: "2"},
		{name: "explicit key in flow", doc: "[? a]", code: yaml.CodeYAMLComplexKeyRefused, line: "1", column: "2"},
		{name: "YAML directive", doc: "%YAML 1.2\n---\na: 1\n", code: yaml.CodeYAMLDirectiveRefused, line: "1", column: "1"},
		{name: "TAG directive", doc: "%TAG ! tag:x,2000:\n---\na: 1\n", code: yaml.CodeYAMLDirectiveRefused, line: "1", column: "1"},
		{name: "duplicate key", doc: "port: 80\nhost: x\nport: 81\n", code: yaml.CodeYAMLDuplicateKey, line: "3", column: "1"},
		{name: "duplicate key across styles", doc: "a: 1\n\"a\": 2\n", code: yaml.CodeYAMLDuplicateKey, line: "2", column: "1"},
		{name: "duplicate key in flow", doc: "{a: 1, a: 2}", code: yaml.CodeYAMLDuplicateKey, line: "1", column: "8"},
		{name: "duplicate key in a large mapping", doc: largeMappingWithDuplicate(), code: yaml.CodeYAMLDuplicateKey, line: "41", column: "1"},
		{name: "leading zero", doc: "mode: 0644\n", code: yaml.CodeYAMLLeadingZeroRefused, line: "1", column: "7"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var out any
		err := yaml.New().Unmarshal([]byte(tc.doc), &out)
		//: the construct's own code is the origin.
		if code, _ := errs.CodeOf(err); code != tc.code {
			t.Fatalf("%s: Unmarshal(%q) error = %v, want code %s", tc.name, tc.doc, err, tc.code)
		}
		//: every refusal is also a decoding failure by code.
		if !errs.HasCode(err, yaml.CodeYAMLUnmarshalFailed) {
			t.Errorf("%s: error %v does not carry %s", tc.name, err, yaml.CodeYAMLUnmarshalFailed)
		}
		//: where the construct starts.
		if line, column := position(err); line != tc.line || column != tc.column {
			t.Errorf("%s: refused at %s:%s, want %s:%s", tc.name, line, column, tc.line, tc.column)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// largeMappingWithDuplicate returns a 40-key mapping whose 41st line repeats
// its first key — past the size where duplicates are checked pairwise.
func largeMappingWithDuplicate() string {
	var b strings.Builder
	//: forty distinct keys.
	for i := range 40 {
		b.WriteString("key" + string(rune('A'+i%26)) + string(rune('a'+i/26)) + ": 1\n")
	}
	//: the first one again.
	b.WriteString("keyAa: 2\n")
	return b.String()
}

// TestALeadingZeroIsTextIntoAString decodes 0644 into a string, where its
// text is its value and no reading of it is ambiguous.
func TestALeadingZeroIsTextIntoAString(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		doc  string
		want string
	}
	tests := []tc{{"octal-looking", "mode: 0644\n", "0644"}, {"zip code", "mode: 01234\n", "01234"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var out struct {
			Mode string `yaml:"mode"`
		}
		if err := yaml.New().Unmarshal([]byte(tc.doc), &out); err != nil {
			t.Fatalf("%s: Unmarshal error = %v", tc.name, err)
		}
		if out.Mode != tc.want {
			t.Errorf("%s: Mode = %q, want %q", tc.name, out.Mode, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestSyntaxErrorsAreLocated decodes malformed documents and checks each is an
// UnmarshalFailed at the position of its fault.
func TestSyntaxErrorsAreLocated(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		doc    string
		line   string
		column string
	}
	tests := []tc{
		{name: "unclosed flow sequence", doc: "a: [1, 2\n", line: "1", column: "4"},
		{name: "unclosed double quote", doc: "a: \"open\n", line: "1", column: "4"},
		{name: "unclosed single quote", doc: "a: 'open\n", line: "1", column: "4"},
		{name: "mapping on the line of its key", doc: "a: b: c\n", line: "1", column: "4"},
		{name: "a deeper line continues a plain scalar", doc: "a: 1\n   b: 2\n", line: "2", column: "5"},
		{name: "unexpected indentation", doc: "a: \"x\"\n  b: 1\n", line: "2", column: "3"},
		{name: "tab indentation", doc: "a:\n\tb: 1\n", line: "2", column: "1"},
		{name: "tab on a blank line", doc: "a: 1\n\t\nb: 2\n", line: "2", column: "1"},
		{name: "tab after a dash", doc: "-\tx\n", line: "1", column: "2"},
		{name: "unknown escape", doc: "a: \"\\q\"\n", line: "1", column: "5"},
		{name: "JSON slash escape", doc: "a: \"\\/\"\n", line: "1", column: "5"},
		{name: "surrogate escape", doc: "a: \"\\uD800\"\n", line: "1", column: "5"},
		{name: "escape past Unicode", doc: "a: \"\\U00110000\"\n", line: "1", column: "5"},
		{name: "escape past a rune", doc: "a: \"\\U80000000\"\n", line: "1", column: "5"},
		{name: "control character", doc: "a: b\x01c\n", line: "1", column: "5"},
		{name: "raw line separator", doc: "a: b\u2028c\n", line: "1", column: "5"},
		{name: "second byte order mark", doc: "\ufeff\ufeff", line: "1", column: "2"},
		{name: "byte order mark opening a line", doc: "\ufeffa: 1\n\ufeffb: 2\n", line: "2", column: "1"},
		{name: "byte order mark in a scalar", doc: "a: b\ufeffc\n", line: "1", column: "5"},
		{name: "invalid UTF-8", doc: "a: \xff\n", line: "1", column: "4"},
		{name: "UTF-16", doc: "\xff\xfea\x00", line: "1", column: "1"},
		{name: "glued comment", doc: "a: \"x\"#c\n", line: "1", column: "7"},
		{name: "content on ---", doc: "--- a\n", line: "1", column: "5"},
		{name: "... closing nothing", doc: "...\n", line: "1", column: "1"},
		{name: "question mark in a flow scalar", doc: "[a?b]", line: "1", column: "3"},
		{name: "multi-line flow key", doc: "[a\n b: c]", line: "1", column: "2"},
		{name: "reserved indicator", doc: "a: @x\n", line: "1", column: "4"},
		{name: "empty key", doc: ": x\n", line: "1", column: "1"},
		{name: "block scalar zero indicator", doc: "a: |0\n  x\n", line: "1", column: "5"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var out any
		err := yaml.New().Unmarshal([]byte(tc.doc), &out)
		//: a decoding failure, as such.
		if !errs.HasReason(err, "UNMARSHAL_FAILED") {
			t.Fatalf("%s: Unmarshal(%q) error = %v, want UNMARSHAL_FAILED", tc.name, tc.doc, err)
		}
		//: where the fault is.
		if line, column := position(err); line != tc.line || column != tc.column {
			t.Errorf("%s: refused at %s:%s, want %s:%s", tc.name, line, column, tc.line, tc.column)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestRefusalsNeverQuoteTheDocument plants a recognisable value in each
// refused position and reads every rendering of the error — its text, its
// public and private messages, every field, every error it unwraps to — for
// it. A refusal says where and why, never what.
func TestRefusalsNeverQuoteTheDocument(t *testing.T) {
	t.Parallel()
	const secret = "hunter2-SECRET"
	type tc struct {
		target any
		name   string
		doc    string
	}
	tests := []tc{
		{name: "anchor", doc: "a: &" + secret + " 1\n"},
		{name: "duplicate key", doc: secret + ": 1\n" + secret + ": 2\n"},
		{name: "syntax", doc: "a: \"" + secret + "\n"},
		{name: "type mismatch", doc: "port: " + secret + "\n", target: &struct {
			Port int `yaml:"port"`
		}{}},
		{name: "YAML 1.1 boolean", doc: "on: " + secret + "\n", target: &struct {
			On bool `yaml:"on"`
		}{}},
		{name: "bad duration", doc: "d: " + secret + "\n", target: &struct {
			D time.Duration `yaml:"d"`
		}{}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		target := tc.target
		if target == nil {
			target = new(any)
		}
		err := yaml.New().Unmarshal([]byte(tc.doc), target)
		if err == nil {
			t.Fatalf("%s: Unmarshal succeeded, want a refusal", tc.name)
		}
		renderings := []string{err.Error(), errs.PublicOf(err), errs.PrivateOf(err)}
		for _, field := range errs.FieldsOf(err) {
			renderings = append(renderings, field.StringValue())
		}
		for cause := errors.Unwrap(err); cause != nil; cause = errors.Unwrap(cause) {
			renderings = append(renderings, cause.Error())
		}
		for _, rendering := range renderings {
			if strings.Contains(rendering, "hunter2") {
				t.Errorf("%s: a rendering of the refusal quotes the document: %q", tc.name, rendering)
			}
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
