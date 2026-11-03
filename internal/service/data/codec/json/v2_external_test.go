package json_test

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	corejson "github.com/kitsunium/sdk/internal/core/data/codec/json"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/json"
)

// v2Shape is a struct whose encoding the two semantics disagree on.
type v2Shape struct {
	Name   string         `json:"name"`
	Nil    []int          `json:"nil"`
	NilMap map[string]int `json:"nil_map"`
	Array  [2]byte        `json:"array"`
	Zero   int            `json:"zero,omitzero"`
	Empty  string         `json:"empty,omitempty"`
	Count  int            `json:"count,omitempty"`
	Map    map[string]int `json:"map"`
}

// TestNewV2SpeaksJSONV2 pins what NewV2 is for (#893): json/v2's semantics,
// next to the registered codec's v1 ones on the same values — and that it
// stays out of the registry.
func TestNewV2SpeaksJSONV2(t *testing.T) {
	t.Parallel()
	v := v2Shape{Name: "n", Array: [2]byte{1, 2}, Map: map[string]int{"b": 2, "a": 1, "c": 3}}
	v2, err := json.NewV2().Marshal(v)
	if err != nil {
		t.Fatalf("v2 Marshal: %v", err)
	}
	if want := `{"name":"n","nil":[],"nil_map":{},"array":"AQI=","count":0,"map":{"a":1,"b":2,"c":3}}`; string(v2) != want {
		t.Fatalf("v2 wrote %s, want %s", v2, want)
	}
	v1, _ := json.New().Marshal(v)
	if want := `{"name":"n","nil":null,"nil_map":null,"array":[1,2],"map":{"a":1,"b":2,"c":3}}`; string(v1) != want {
		t.Fatalf("v1 wrote %s, want %s", v1, want)
	}
	var got v2Shape
	if err := json.NewV2().Unmarshal([]byte(`{"NAME":"x"}`), &got); err != nil || got.Name != "" {
		t.Fatalf("v2 matched a case-folded name: %+v, %v", got, err)
	}
	for _, bad := range []string{`{"name":"a","name":"b"}`, "{\"name\":\"\xff\"}"} {
		if err := json.NewV2().Unmarshal([]byte(bad), &got); !errs.HasCode(err, corejson.CodeJSONUnmarshalFailed) {
			t.Fatalf("v2 read %q: %v, want UNMARSHAL_FAILED", bad, err)
		}
	}
	if _, err := json.NewV2().Marshal(struct{ D time.Duration }{time.Second}); !errs.HasCode(err, corejson.CodeJSONMarshalFailed) {
		t.Fatalf("v2 wrote a time.Duration without a format: %v", err)
	}
	if c, ok := corecodec.Lookup("json"); !ok || c != json.New() {
		t.Fatal("NewV2 changed what the registry resolves json to")
	}
}

// TestNewV2AppendsAndStreams pins NewV2's Appender and stream: Append writes
// Marshal's bytes after dst and leaves dst on failure; the encoder writes one
// value and a newline per Encode, the decoder reads them back, More false and
// io.EOF unwrapped at the end.
func TestNewV2AppendsAndStreams(t *testing.T) {
	t.Parallel()
	c := json.NewV2()
	a, ok := c.(corecodec.Appender)
	if !ok {
		t.Fatal("NewV2 is not an Appender")
	}
	want, _ := c.Marshal(map[string]int{"k": 1})
	if got, err := a.Append([]byte("PRE"), map[string]int{"k": 1}); err != nil || !bytes.Equal(got[3:], want) {
		t.Fatalf("Append wrote %q, %v", got, err)
	}
	if got, err := a.Append([]byte("PRE"), make(chan int)); err == nil || string(got) != "PRE" {
		t.Fatalf("Append of a channel: %q, %v", got, err)
	}
	sc, ok := c.(corecodec.StreamingCodec)
	if !ok {
		t.Fatal("NewV2 is not a StreamingCodec")
	}
	var buf bytes.Buffer
	enc := sc.NewEncoder(&buf)
	for _, v := range []any{1, "two", []int(nil)} {
		if err := enc.Encode(v); err != nil {
			t.Fatalf("Encode: %v", err)
		}
	}
	if err := enc.Close(); err != nil || buf.String() != "1\n\"two\"\n[]\n" {
		t.Fatalf("stream wrote %q, %v", buf.String(), err)
	}
	dec := sc.NewDecoder(strings.NewReader(buf.String()))
	n := 0
	for dec.More() {
		var v any
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		n++
	}
	var v any
	if err := dec.Decode(&v); err != io.EOF || n != 3 {
		t.Fatalf("decoded %d values, then %v", n, err)
	}
	if err := sc.NewDecoder(strings.NewReader(`{"a":`)).Decode(&v); !errs.HasCode(err, corejson.CodeJSONUnmarshalFailed) {
		t.Fatalf("a truncated value: %v, want UNMARSHAL_FAILED", err)
	}
}
