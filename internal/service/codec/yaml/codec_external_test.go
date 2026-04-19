package yaml_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/yaml"
)

type payload struct {
	Name string `yaml:"name"`
	Age  int    `yaml:"age"`
}

func TestNew(t *testing.T) {
	t.Parallel()
	c := yaml.New()
	if c == nil {
		t.Fatal("New() returned nil")
	}
	if c.Name() != "yaml" {
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
	data, err := yaml.New().Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out payload
	if uerr := yaml.New().Unmarshal(data, &out); uerr != nil {
		t.Fatalf("Unmarshal: %v", uerr)
	}
	if out != in {
		t.Errorf("got %+v want %+v", out, in)
	}
}

func TestUnmarshal_Malformed(t *testing.T) {
	t.Parallel()
	var out payload
	err := yaml.New().Unmarshal([]byte("name: [unclosed"), &out)
	if !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Errorf("expected UNMARSHAL_FAILED, got %v", err)
	}
}

func TestStreaming(t *testing.T) {
	t.Parallel()
	sc := yaml.New().(codec.StreamingCodec)
	var buf bytes.Buffer
	enc := sc.NewEncoder(&buf)
	for _, p := range []payload{{Name: "a", Age: 1}, {Name: "b", Age: 2}} {
		if err := enc.Encode(p); err != nil {
			t.Fatalf("Encode: %v", err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	dec := sc.NewDecoder(&buf)
	var count int
	for dec.More() {
		var p payload
		err := dec.Decode(&p)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		count++
	}
	if count != 2 {
		t.Errorf("decoded %d records, want 2", count)
	}
}

func TestStreaming_DecodeError(t *testing.T) {
	t.Parallel()
	sc := yaml.New().(codec.StreamingCodec)
	dec := sc.NewDecoder(strings.NewReader("name: [bad"))
	var p payload
	if err := dec.Decode(&p); !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Errorf("expected UNMARSHAL_FAILED, got %v", err)
	}
}

func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	if _, ok := codec.Lookup(codec.Format("yaml")); !ok {
		t.Error("yaml codec not registered")
	}
	if _, ok := codec.LookupMIME("application/yaml"); !ok {
		t.Error("application/yaml not resolved")
	}
	if _, ok := codec.LookupExt(".yaml"); !ok {
		t.Error(".yaml not resolved")
	}
	if _, ok := codec.LookupExt(".yml"); !ok {
		t.Error(".yml not resolved")
	}
}
