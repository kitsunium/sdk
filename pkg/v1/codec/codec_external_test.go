package codec_test

import (
	"bytes"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/codec"
)

type event struct {
	Name  string `json:"name" xml:"name"`
	Count int    `json:"count" xml:"count"`
}

func TestMarshalUnmarshal_JSON(t *testing.T) {
	t.Parallel()
	in := event{Name: "ping", Count: 3}
	data, err := codec.Marshal(codec.JSON, in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), "\"name\":\"ping\"") {
		t.Errorf("unexpected payload: %s", data)
	}
	var out event
	if uerr := codec.Unmarshal(codec.JSON, data, &out); uerr != nil {
		t.Fatalf("Unmarshal: %v", uerr)
	}
	if out != in {
		t.Errorf("got %+v want %+v", out, in)
	}
}

func TestMarshal_UnknownFormat(t *testing.T) {
	t.Parallel()
	_, err := codec.Marshal(codec.Format("nope"), 1)
	if !errs.HasReason(err, "UNKNOWN_FORMAT") {
		t.Errorf("expected UNKNOWN_FORMAT, got %v", err)
	}
}

func TestUnmarshal_UnknownFormat(t *testing.T) {
	t.Parallel()
	var v int
	err := codec.Unmarshal(codec.Format("nope"), []byte("1"), &v)
	if !errs.HasReason(err, "UNKNOWN_FORMAT") {
		t.Errorf("expected UNKNOWN_FORMAT, got %v", err)
	}
}

func TestStreaming_JSON(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	enc, err := codec.NewEncoder(codec.JSON, &buf)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	for _, e := range []event{{Name: "a", Count: 1}, {Name: "b", Count: 2}} {
		if eerr := enc.Encode(e); eerr != nil {
			t.Fatalf("Encode: %v", eerr)
		}
	}
	if cerr := enc.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	dec, derr := codec.NewDecoder(codec.JSON, &buf)
	if derr != nil {
		t.Fatalf("NewDecoder: %v", derr)
	}
	var got []event
	for dec.More() {
		var e event
		if err := dec.Decode(&e); err != nil && err != io.EOF {
			t.Fatalf("Decode: %v", err)
		}
		got = append(got, e)
	}
	if len(got) != 2 {
		t.Fatalf("decoded %d records, want 2", len(got))
	}
}

func TestNewEncoder_UnknownFormat(t *testing.T) {
	t.Parallel()
	_, err := codec.NewEncoder(codec.Format("nope"), io.Discard)
	if !errs.HasReason(err, "UNKNOWN_FORMAT") {
		t.Errorf("expected UNKNOWN_FORMAT, got %v", err)
	}
}

func TestNewDecoder_UnknownFormat(t *testing.T) {
	t.Parallel()
	_, err := codec.NewDecoder(codec.Format("nope"), strings.NewReader(""))
	if !errs.HasReason(err, "UNKNOWN_FORMAT") {
		t.Errorf("expected UNKNOWN_FORMAT, got %v", err)
	}
}

func TestNewEncoder_StreamingUnsupported(t *testing.T) {
	t.Parallel()
	//: ASN.1 DER codec is registered but does not implement StreamingCodec.
	_, err := codec.NewEncoder(codec.ASN1DER, io.Discard)
	if !errs.HasReason(err, "STREAMING_UNSUPPORTED") {
		t.Errorf("expected STREAMING_UNSUPPORTED, got %v", err)
	}
}

func TestNewDecoder_StreamingUnsupported(t *testing.T) {
	t.Parallel()
	_, err := codec.NewDecoder(codec.CSV, strings.NewReader(""))
	if !errs.HasReason(err, "STREAMING_UNSUPPORTED") {
		t.Errorf("expected STREAMING_UNSUPPORTED, got %v", err)
	}
}

func TestFromMIME(t *testing.T) {
	t.Parallel()
	f, ok := codec.FromMIME("application/json")
	if !ok || f != codec.JSON {
		t.Errorf("FromMIME(application/json) = %q, %v", f, ok)
	}
	if _, ok := codec.FromMIME("unknown"); ok {
		t.Error("unknown MIME should not resolve")
	}
}

func TestFromMIME_WithParameters(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
		want codec.Format
	}
	tests := []tc{
		{"charset param", "application/json; charset=utf-8", codec.JSON},
		{"uppercase header", "APPLICATION/JSON", codec.JSON},
		{"trailing whitespace", "application/xml ", codec.XML},
		{"multiple params", "application/cbor; boundary=xyz; q=0.9", codec.CBOR},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, ok := codec.FromMIME(c.in)
		if !ok || got != c.want {
			t.Errorf("%s: FromMIME(%q) = (%q, %v), want (%q, true)",
				c.name, c.in, got, ok, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestFromExtension(t *testing.T) {
	t.Parallel()
	f, ok := codec.FromExtension(".xml")
	if !ok || f != codec.XML {
		t.Errorf("FromExtension(.xml) = %q, %v", f, ok)
	}
	if _, ok := codec.FromExtension(".zzz"); ok {
		t.Error("unknown extension should not resolve")
	}
}

func TestAvailable(t *testing.T) {
	t.Parallel()
	list := codec.Available()
	required := []codec.Format{codec.JSON, codec.NDJSON, codec.XML, codec.CSV, codec.ASN1DER, codec.PEM}
	for _, want := range required {
		if !slices.Contains(list, want) {
			t.Errorf("Available() missing %q (got %v)", want, list)
		}
	}
}
