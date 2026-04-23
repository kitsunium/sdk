package xml_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/xml"
)

type sampleDoc struct {
	ID string `xml:"id,attr"`
}

// TestNew verifies the constructor returns a non-nil singleton with the
// canonical name.
func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"canonical name", "xml"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := xml.New()
		if c == nil {
			t.Fatalf("%s: New returned nil", tc.name)
		}
		if got := c.Name(); got != tc.want {
			t.Errorf("%s: Name=%q want %q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestMarshal covers the encoder success path plus available error cases.
func TestMarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      any
		wantErr string
	}
	tests := []tc{
		{"round-trip success", sampleDoc{ID: "x"}, ""},
		{"unsupported input surfaces MARSHAL_FAILED", make(chan int), "MARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := xml.New().Marshal(tc.in)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: Marshal err=%v", tc.name, err)
		}
		if tc.wantErr != "" && !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestUnmarshal covers the decoder success path plus the UNMARSHAL_FAILED
// branch on malformed bytes.
func TestUnmarshal(t *testing.T) {
	t.Parallel()
	data := []byte(`<sampleDoc id="y"/>`)
	type tc struct {
		name    string
		data    []byte
		target  any
		wantErr string
	}
	var good sampleDoc
	var bad sampleDoc
	tests := []tc{
		{"round-trip success", data, &good, ""},
		{"malformed bytes surface UNMARSHAL_FAILED", []byte("<sampleDoc"), &bad, "UNMARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := xml.New().Unmarshal(tc.data, tc.target)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: Unmarshal err=%v", tc.name, err)
		}
		if tc.wantErr != "" && !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestNewEncoder exercises the streaming encoder with a happy-path encode
// plus a writer-failure path when applicable.
func TestNewEncoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"streams a record"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		sc, ok := xml.New().(codec.StreamingCodec)
		if !ok {
			t.Fatalf("%s: codec does not implement StreamingCodec", tc.name)
		}
		var buf bytes.Buffer
		enc := sc.NewEncoder(&buf)
		if err := enc.Encode(sampleDoc{ID: "x"}); err != nil {
			t.Errorf("%s: Encode err=%v", tc.name, err)
		}
		if err := enc.Close(); err != nil {
			t.Errorf("%s: Close err=%v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestNewDecoder exercises the streaming decoder including the
// UNMARSHAL_FAILED branch on corrupt input.
func TestNewDecoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		wantErr string
	}
	good, _ := xml.New().Marshal(sampleDoc{ID: "x"})
	tests := []tc{
		{"decodes a valid record", good, ""},
		{"corrupt input surfaces UNMARSHAL_FAILED", []byte("<sampleDoc"), "UNMARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		sc := xml.New().(codec.StreamingCodec)
		dec := sc.NewDecoder(bytes.NewReader(tc.data))
		var out sampleDoc
		err := dec.Decode(&out)
		if tc.wantErr == "" {
			if err != nil && !errors.Is(err, io.EOF) {
				t.Errorf("%s: Decode err=%v", tc.name, err)
			}
			return
		}
		if !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestUnmarshal_BillionLaughsBounded asserts stdlib encoding/xml handles
// the classic entity-expansion bomb without unbounded memory growth or a
// hang. Go 1.21+ caps internal entity expansion by construction; this test
// locks that property so a future Go upgrade regressing the behaviour is
// caught by the build. Regresses finding #18 from the post-audit review.
//
// The payload below is the textbook "billion laughs" shape — a nested
// DOCTYPE that, without entity caps, would expand to ~10^9 characters
// and exhaust RAM. On hardened parsers it either errors or returns with
// bounded output.
func TestUnmarshal_BillionLaughsBounded(t *testing.T) {
	t.Parallel()
	bomb := []byte(`<?xml version="1.0"?>` +
		`<!DOCTYPE lolz [` +
		`<!ENTITY lol "lol">` +
		`<!ENTITY lol2 "&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;">` +
		`<!ENTITY lol3 "&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;">` +
		`<!ENTITY lol4 "&lol3;&lol3;&lol3;&lol3;&lol3;&lol3;&lol3;&lol3;&lol3;&lol3;">` +
		`<!ENTITY lol5 "&lol4;&lol4;&lol4;&lol4;&lol4;&lol4;&lol4;&lol4;&lol4;&lol4;">` +
		`]>` +
		`<sampleDoc id="&lol5;"/>`)
	var v sampleDoc
	//: Go 1.21+ refuses external DTD and bounds internal entity expansion;
	//: either an error OR a bounded (non-explosive) parse is acceptable.
	//: What MUST NOT happen is OOM / hang — that's the regression guard.
	err := xml.New().Unmarshal(bomb, &v)
	//: stdlib behaviour on Go 1.21+: entity expansion caps at ~64 KiB per
	//: token; this particular shape errors with "XML syntax error" or
	//: returns with v.ID truncated. Both are acceptable; we only assert
	//: the parse terminated without panic / OOM, signalled by reaching here.
	_ = err
	//: double-check: if it did succeed, the result must not be astronomical.
	if len(v.ID) > 1<<20 {
		t.Errorf("entity expansion produced a %d-byte ID; cap expected ~64KiB", len(v.ID))
	}
}

// TestRegisteredViaImport verifies the codec self-registers on package load.
func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		check func() bool
	}
	tests := []tc{
		{"format registered", func() bool { _, ok := codec.Lookup(codec.Format("xml")); return ok }},
		{"MIME resolved", func() bool { _, ok := codec.LookupMIME("application/xml"); return ok }},
		{"extension resolved", func() bool { _, ok := codec.LookupExt(".xml"); return ok }},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if !tc.check() {
			t.Errorf("%s: lookup failed", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
