package pem_test

import (
	stdpem "encoding/pem"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/pem"
)

func TestNew(t *testing.T) {
	t.Parallel()
	c := pem.New()
	if c == nil {
		t.Fatal("New() returned nil")
	}
	if c.Name() != "pem" {
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
	in := &stdpem.Block{Type: "TEST", Bytes: []byte("hello world")}
	data, err := pem.New().Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out *stdpem.Block
	if uerr := pem.New().Unmarshal(data, &out); uerr != nil {
		t.Fatalf("Unmarshal: %v", uerr)
	}
	if out == nil || out.Type != in.Type || string(out.Bytes) != string(in.Bytes) {
		t.Errorf("round-trip: got %+v want %+v", out, in)
	}
}

func TestMarshal_WrongType(t *testing.T) {
	t.Parallel()
	_, err := pem.New().Marshal("nope")
	if !errs.HasReason(err, "VALUE_INVALID") {
		t.Errorf("expected VALUE_INVALID, got %v", err)
	}
}

func TestUnmarshal_WrongTarget(t *testing.T) {
	t.Parallel()
	var s string
	err := pem.New().Unmarshal([]byte("x"), &s)
	if !errs.HasReason(err, "VALUE_INVALID") {
		t.Errorf("expected VALUE_INVALID, got %v", err)
	}
}

func TestUnmarshal_NoBlock(t *testing.T) {
	t.Parallel()
	var out *stdpem.Block
	err := pem.New().Unmarshal([]byte("not a pem"), &out)
	if !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Errorf("expected UNMARSHAL_FAILED, got %v", err)
	}
}

func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	if _, ok := codec.Lookup(codec.Format("pem")); !ok {
		t.Error("pem codec not registered")
	}
	if _, ok := codec.LookupMIME("application/x-pem-file"); !ok {
		t.Error("application/x-pem-file not resolved")
	}
	if _, ok := codec.LookupExt(".pem"); !ok {
		t.Error(".pem not resolved")
	}
}
