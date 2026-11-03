package codec

import (
	"bytes"
	stdjson "encoding/json"
	stdpem "encoding/pem"
	"net/url"
	"testing"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
)

// precheckRecord is a struct no constrained codec takes natively.
type precheckRecord struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// namedValues and namedRows are user types over the native shapes.
type (
	namedValues map[string][]string
	namedRows   []precheckRecord
)

// precheckCorpus holds values of every kind the four pre-checked Formats meet.
func precheckCorpus() map[string]any {
	rows := []precheckRecord{{1, "a"}}
	table := [][]string{{"a"}, {"1"}}
	values := url.Values{"a": {"1"}}
	single := map[string]string{"a": "1"}
	return map[string]any{
		"nil":                  nil,
		"struct":               precheckRecord{1, "a"},
		"pointer to struct":    &precheckRecord{1, "a"},
		"int":                  7,
		"string":               "s",
		"map[string]any":       map[string]any{"a": 1},
		"map[string]string":    single,
		"*map[string]string":   &single,
		"url.Values":           values,
		"*url.Values":          &values,
		"nil *url.Values":      (*url.Values)(nil),
		"map[string][]string":  map[string][]string{"a": {"1"}},
		"named values":         namedValues{"a": {"1"}},
		"[][]string":           table,
		"*[][]string":          &table,
		"[]struct":             rows,
		"*[]struct":            &rows,
		"**[]struct":           new(*[]precheckRecord),
		"named rows":           namedRows(rows),
		"[1]struct":            [1]precheckRecord{{1, "a"}},
		"[]int":                []int{1},
		"[]json.RawMessage":    []stdjson.RawMessage{stdjson.RawMessage(`{"a":1}`)},
		"[][]byte":             [][]byte{[]byte(`{"a":1}`)},
		"pem.Block":            stdpem.Block{Type: "X"},
		"*pem.Block":           &stdpem.Block{Type: "X", Bytes: []byte{1}},
		"nil *pem.Block":       (*stdpem.Block)(nil),
		"[]*pem.Block":         []*stdpem.Block{{Type: "X"}},
		"chan":                 make(chan int),
		"func":                 func() {},
		"[]byte":               []byte("x"),
		"map[int]string":       map[int]string{1: "a"},
		"pointer to map[any]":  &map[string]any{"a": 1},
		"pointer to int":       new(int),
		"nil pointer to slice": (*[]precheckRecord)(nil),
	}
}

// TestRefusesShapeMirrorsTheCodec is #910's safety property: whatever
// refusesShape sends straight to promotion, the codec itself refuses as a
// shape mismatch — so the pre-check changes no output, only skips building an
// error to discard. And what it promotes, promoteMarshal encodes byte for
// byte as the error-driven path did.
func TestRefusesShapeMirrorsTheCodec(t *testing.T) {
	t.Parallel()
	for _, f := range []Format{Form, PEM, NDJSON, CSV} {
		c, ok := corecodec.Lookup(f)
		if !ok {
			t.Fatalf("%s: not registered", f)
		}
		for name, v := range precheckCorpus() {
			if !refusesShape(f, v) {
				continue
			}
			_, err := c.Marshal(v)
			if err == nil || !isValueShapeMismatch(f, err) {
				t.Errorf("%s %s: refusesShape is true but the codec returns %v", f, name, err)
				continue
			}
			want, werr := promoteMarshal(f, c, v)
			got, gerr := Marshal(f, v)
			if !bytes.Equal(got, want) || (werr == nil) != (gerr == nil) {
				t.Errorf("%s %s: Marshal = %q, %v; the promotion = %q, %v", f, name, got, gerr, want, werr)
			}
		}
	}
}

// TestRefusesShapeLeavesTheRestToTheCodec: flatbuffers, tlv and the formats
// that take any value are never pre-judged, and the native shapes of the four
// pre-checked ones go to their codec.
func TestRefusesShapeLeavesTheRestToTheCodec(t *testing.T) {
	t.Parallel()
	for _, f := range []Format{FlatBuffers, TLV, JSON, CBOR} {
		if refusesShape(f, precheckRecord{}) {
			t.Errorf("%s: a struct is pre-judged", f)
		}
	}
	natives := map[Format]any{
		Form:   url.Values{"a": {"1"}},
		PEM:    &stdpem.Block{Type: "X"},
		NDJSON: []precheckRecord{{1, "a"}},
		CSV:    [][]string{{"a"}},
	}
	for f, v := range natives {
		if refusesShape(f, v) {
			t.Errorf("%s: its native shape %T is pre-judged", f, v)
		}
	}
}
