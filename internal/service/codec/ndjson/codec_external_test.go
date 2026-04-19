package ndjson_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/ndjson"
)

type payload struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func TestNew(t *testing.T) {
	t.Parallel()
	c := ndjson.New()
	if c == nil {
		t.Fatal("New() returned nil")
	}
	if c.Name() != "ndjson" {
		t.Errorf("Name = %q", c.Name())
	}
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	in := []payload{{Name: "a", Age: 1}, {Name: "b", Age: 2}}
	data, err := ndjson.New().Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	text := string(data)
	if !strings.HasSuffix(text, "\n") {
		t.Error("output missing trailing newline")
	}
	if strings.Count(text, "\n") != len(in) {
		t.Errorf("wrong newline count in %q", text)
	}
	var out []payload
	if uerr := ndjson.New().Unmarshal(data, &out); uerr != nil {
		t.Fatalf("Unmarshal: %v", uerr)
	}
	if len(out) != len(in) || out[0] != in[0] || out[1] != in[1] {
		t.Errorf("round-trip: got %+v want %+v", out, in)
	}
}

func TestUnmarshal_SkipBlankLines(t *testing.T) {
	t.Parallel()
	data := []byte("{\"name\":\"a\",\"age\":1}\n\n\n{\"name\":\"b\",\"age\":2}\n")
	var out []payload
	if err := ndjson.New().Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d records, want 2", len(out))
	}
}

func TestMarshal_NotSlice(t *testing.T) {
	t.Parallel()
	_, err := ndjson.New().Marshal(42)
	if !errs.HasReason(err, "VALUE_INVALID") {
		t.Errorf("expected VALUE_INVALID, got %v", err)
	}
}

func TestUnmarshal_NotSlicePointer(t *testing.T) {
	t.Parallel()
	var s string
	err := ndjson.New().Unmarshal([]byte("{}\n"), &s)
	if !errs.HasReason(err, "VALUE_INVALID") {
		t.Errorf("expected VALUE_INVALID, got %v", err)
	}
}

func TestUnmarshal_NilPointer(t *testing.T) {
	t.Parallel()
	var target *[]payload
	err := ndjson.New().Unmarshal([]byte("{}\n"), target)
	if !errs.HasReason(err, "VALUE_INVALID") {
		t.Errorf("expected VALUE_INVALID, got %v", err)
	}
}

func TestUnmarshal_BadLine(t *testing.T) {
	t.Parallel()
	var out []payload
	err := ndjson.New().Unmarshal([]byte("not json\n"), &out)
	if !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Errorf("expected UNMARSHAL_FAILED, got %v", err)
	}
}

func TestMarshal_BadElement(t *testing.T) {
	t.Parallel()
	//: channels aren't JSON-serialisable.
	_, err := ndjson.New().Marshal([]chan int{make(chan int)})
	if !errs.HasReason(err, "MARSHAL_FAILED") {
		t.Errorf("expected MARSHAL_FAILED, got %v", err)
	}
}

func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	if _, ok := codec.Lookup(codec.Format("ndjson")); !ok {
		t.Error("ndjson codec not registered")
	}
	if _, ok := codec.LookupMIME("application/x-ndjson"); !ok {
		t.Error("application/x-ndjson not resolved")
	}
	if _, ok := codec.LookupExt(".ndjson"); !ok {
		t.Error(".ndjson not resolved")
	}
}
