package toml_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/toml"
)

type payload struct {
	Name string `toml:"name"`
	Age  int    `toml:"age"`
}

func TestNew(t *testing.T) {
	t.Parallel()
	c := toml.New()
	if c == nil {
		t.Fatal("New() returned nil")
	}
	if c.Name() != "toml" {
		t.Errorf("Name = %q", c.Name())
	}
	if len(c.MIMETypes()) == 0 {
		t.Error("MIMETypes empty")
	}
	if len(c.Extensions()) == 0 {
		t.Error("Extensions empty")
	}
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	in := payload{Name: "ping", Age: 3}
	data, err := toml.New().Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out payload
	if uerr := toml.New().Unmarshal(data, &out); uerr != nil {
		t.Fatalf("Unmarshal: %v", uerr)
	}
	if out != in {
		t.Errorf("got %+v want %+v", out, in)
	}
}

func TestUnmarshal_Malformed(t *testing.T) {
	t.Parallel()
	var out payload
	err := toml.New().Unmarshal([]byte("name = [unclosed"), &out)
	if !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Errorf("expected UNMARSHAL_FAILED, got %v", err)
	}
}

func TestStreaming(t *testing.T) {
	t.Parallel()
	sc := toml.New().(codec.StreamingCodec)
	var buf bytes.Buffer
	enc := sc.NewEncoder(&buf)
	if err := enc.Encode(payload{Name: "a", Age: 1}); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	dec := sc.NewDecoder(&buf)
	if !dec.More() {
		t.Error("More() should be true before Decode")
	}
	var p payload
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if dec.More() {
		t.Error("More() should be false after Decode")
	}
	if err := dec.Decode(&p); err != io.EOF {
		t.Errorf("second Decode err = %v, want io.EOF", err)
	}
}

func TestStreaming_DecodeError(t *testing.T) {
	t.Parallel()
	sc := toml.New().(codec.StreamingCodec)
	dec := sc.NewDecoder(strings.NewReader("name = [bad"))
	var p payload
	if err := dec.Decode(&p); !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Errorf("expected UNMARSHAL_FAILED, got %v", err)
	}
}

func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	if _, ok := codec.Lookup(codec.Format("toml")); !ok {
		t.Error("toml codec not registered")
	}
	if _, ok := codec.LookupMIME("application/toml"); !ok {
		t.Error("application/toml not resolved")
	}
	if _, ok := codec.LookupExt(".toml"); !ok {
		t.Error(".toml not resolved")
	}
}
