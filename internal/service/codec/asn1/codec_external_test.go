package asn1_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/asn1"
)

func TestNew(t *testing.T) {
	t.Parallel()
	c := asn1.New()
	if c == nil {
		t.Fatal("New() returned nil")
	}
	if c.Name() != "asn1-der" {
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
	type payload struct {
		N int
		S string
	}
	in := payload{N: 42, S: "hello"}
	data, err := asn1.New().Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out payload
	if uerr := asn1.New().Unmarshal(data, &out); uerr != nil {
		t.Fatalf("Unmarshal: %v", uerr)
	}
	if out != in {
		t.Errorf("round-trip: got %+v want %+v", out, in)
	}
}

func TestUnmarshal_Invalid(t *testing.T) {
	t.Parallel()
	var out struct{ N int }
	err := asn1.New().Unmarshal([]byte{0xff, 0x00}, &out)
	if !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Errorf("expected UNMARSHAL_FAILED, got %v", err)
	}
}

func TestMarshal_Invalid(t *testing.T) {
	t.Parallel()
	//: channels aren't representable in ASN.1.
	_, err := asn1.New().Marshal(make(chan int))
	if !errs.HasReason(err, "MARSHAL_FAILED") {
		t.Errorf("expected MARSHAL_FAILED, got %v", err)
	}
}

func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	if _, ok := codec.Lookup(codec.Format("asn1-der")); !ok {
		t.Error("asn1-der codec not registered")
	}
	if _, ok := codec.LookupMIME("application/pkix-cert"); !ok {
		t.Error("application/pkix-cert not resolved")
	}
	if _, ok := codec.LookupExt(".der"); !ok {
		t.Error(".der not resolved")
	}
}
