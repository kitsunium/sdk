package xml_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/xml"
)

type sampleDoc struct {
	ID string `xml:"id,attr"`
}

func TestNew(t *testing.T) {
	t.Parallel()
	c := xml.New()
	if c == nil {
		t.Fatal("New() returned nil")
	}
	if c.Name() != "xml" {
		t.Errorf("Name = %q", c.Name())
	}
}

func TestMarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   sampleDoc
		want string
	}
	tests := []tc{
		{"attribute-only", sampleDoc{ID: "x"}, `<sampleDoc id="x"></sampleDoc>`},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		data, err := xml.New().Marshal(c.in)
		if err != nil {
			t.Fatalf("%s: Marshal err = %v", c.name, err)
		}
		if !strings.Contains(string(data), c.want) {
			t.Errorf("%s: %q missing %q", c.name, data, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestUnmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    string
		wantErr bool
	}
	tests := []tc{
		{"valid", `<sampleDoc id="y"/>`, false},
		{"malformed", `<sampleDoc`, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var got sampleDoc
		err := xml.New().Unmarshal([]byte(c.data), &got)
		if (err != nil) != c.wantErr {
			t.Fatalf("%s: err=%v wantErr=%v", c.name, err, c.wantErr)
		}
		if c.wantErr && !errs.HasReason(err, "UNMARSHAL_FAILED") {
			t.Errorf("%s: missing UNMARSHAL_FAILED reason: %v", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestMarshal_Invalid(t *testing.T) {
	t.Parallel()
	//: channels aren't XML-serialisable.
	_, err := xml.New().Marshal(make(chan int))
	if !errs.HasReason(err, "MARSHAL_FAILED") {
		t.Errorf("expected MARSHAL_FAILED, got %v", err)
	}
}

func TestMIMEAndExtensions(t *testing.T) {
	t.Parallel()
	c := xml.New()
	if len(c.MIMETypes()) == 0 {
		t.Error("MIMETypes empty")
	}
	if len(c.Extensions()) == 0 {
		t.Error("Extensions empty")
	}
}

func TestStreaming(t *testing.T) {
	t.Parallel()
	c := xml.New()
	sc, ok := c.(codec.StreamingCodec)
	if !ok {
		t.Fatal("xml codec does not implement StreamingCodec")
	}
	var buf bytes.Buffer
	enc := sc.NewEncoder(&buf)
	for _, d := range []sampleDoc{{ID: "a"}, {ID: "b"}} {
		if err := enc.Encode(d); err != nil {
			t.Fatalf("Encode: %v", err)
		}
	}
	if cerr := enc.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	dec := sc.NewDecoder(&buf)
	var decoded []sampleDoc
	for dec.More() {
		var d sampleDoc
		err := dec.Decode(&d)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		decoded = append(decoded, d)
	}
	if len(decoded) != 2 || decoded[0].ID != "a" || decoded[1].ID != "b" {
		t.Errorf("decoded %+v, want [{a} {b}]", decoded)
	}
}

func TestStreaming_EncodeError(t *testing.T) {
	t.Parallel()
	sc := xml.New().(codec.StreamingCodec)
	enc := sc.NewEncoder(io.Discard)
	if err := enc.Encode(make(chan int)); !errs.HasReason(err, "MARSHAL_FAILED") {
		t.Errorf("expected MARSHAL_FAILED, got %v", err)
	}
}

func TestStreaming_DecodeError(t *testing.T) {
	t.Parallel()
	sc := xml.New().(codec.StreamingCodec)
	dec := sc.NewDecoder(strings.NewReader("<bad"))
	var d sampleDoc
	if err := dec.Decode(&d); !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Errorf("expected UNMARSHAL_FAILED, got %v", err)
	}
}

func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	if _, ok := codec.Lookup(codec.Format("xml")); !ok {
		t.Error("xml codec not registered")
	}
	if _, ok := codec.LookupMIME("application/xml"); !ok {
		t.Error("application/xml not resolved")
	}
	if _, ok := codec.LookupExt(".xml"); !ok {
		t.Error(".xml not resolved")
	}
}
