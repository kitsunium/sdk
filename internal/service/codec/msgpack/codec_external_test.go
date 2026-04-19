package msgpack_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/msgpack"
)

type payload struct {
	Name string `msgpack:"name"`
	Age  int    `msgpack:"age"`
}

func TestNew(t *testing.T) {
	t.Parallel()
	c := msgpack.New()
	if c == nil {
		t.Fatal("New() returned nil")
	}
	if c.Name() != "msgpack" {
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
	data, err := msgpack.New().Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out payload
	if uerr := msgpack.New().Unmarshal(data, &out); uerr != nil {
		t.Fatalf("Unmarshal: %v", uerr)
	}
	if out != in {
		t.Errorf("got %+v want %+v", out, in)
	}
}

func TestUnmarshal_Malformed(t *testing.T) {
	t.Parallel()
	var out payload
	err := msgpack.New().Unmarshal([]byte{0xc1}, &out)
	if !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Errorf("expected UNMARSHAL_FAILED, got %v", err)
	}
}

func TestMarshal_Invalid(t *testing.T) {
	t.Parallel()
	//: channels aren't representable in MessagePack.
	_, err := msgpack.New().Marshal(make(chan int))
	if !errs.HasReason(err, "MARSHAL_FAILED") {
		t.Errorf("expected MARSHAL_FAILED, got %v", err)
	}
}

func TestStreaming(t *testing.T) {
	t.Parallel()
	sc := msgpack.New().(codec.StreamingCodec)
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
	sc := msgpack.New().(codec.StreamingCodec)
	dec := sc.NewDecoder(bytes.NewReader([]byte{0xc1, 0xc1}))
	var p payload
	if err := dec.Decode(&p); !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Errorf("expected UNMARSHAL_FAILED, got %v", err)
	}
}

func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	if _, ok := codec.Lookup(codec.Format("msgpack")); !ok {
		t.Error("msgpack codec not registered")
	}
	if _, ok := codec.LookupMIME("application/msgpack"); !ok {
		t.Error("application/msgpack not resolved")
	}
	if _, ok := codec.LookupExt(".msgpack"); !ok {
		t.Error(".msgpack not resolved")
	}
}
