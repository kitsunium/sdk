package cbor_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/cbor"
)

type payload struct {
	Name string `cbor:"name"`
	Age  int    `cbor:"age"`
}

func TestNew(t *testing.T) {
	t.Parallel()
	c := cbor.New()
	if c == nil {
		t.Fatal("New() returned nil")
	}
	if c.Name() != "cbor" {
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
	data, err := cbor.New().Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out payload
	if uerr := cbor.New().Unmarshal(data, &out); uerr != nil {
		t.Fatalf("Unmarshal: %v", uerr)
	}
	if out != in {
		t.Errorf("got %+v want %+v", out, in)
	}
}

func TestUnmarshal_Malformed(t *testing.T) {
	t.Parallel()
	var out payload
	err := cbor.New().Unmarshal([]byte{0xff, 0xff, 0xff}, &out)
	if !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Errorf("expected UNMARSHAL_FAILED, got %v", err)
	}
}

func TestMarshal_Invalid(t *testing.T) {
	t.Parallel()
	//: channels aren't representable in CBOR.
	_, err := cbor.New().Marshal(make(chan int))
	if !errs.HasReason(err, "MARSHAL_FAILED") {
		t.Errorf("expected MARSHAL_FAILED, got %v", err)
	}
}

func TestStreaming(t *testing.T) {
	t.Parallel()
	sc := cbor.New().(codec.StreamingCodec)
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
	count := 0
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
	sc := cbor.New().(codec.StreamingCodec)
	dec := sc.NewDecoder(bytes.NewReader([]byte{0xff, 0x00}))
	var p payload
	if err := dec.Decode(&p); !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Errorf("expected UNMARSHAL_FAILED, got %v", err)
	}
}

func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	if _, ok := codec.Lookup(codec.Format("cbor")); !ok {
		t.Error("cbor codec not registered")
	}
	if _, ok := codec.LookupMIME("application/cbor"); !ok {
		t.Error("application/cbor not resolved")
	}
	if _, ok := codec.LookupExt(".cbor"); !ok {
		t.Error(".cbor not resolved")
	}
}
