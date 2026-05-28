package pem_test

import (
	"bytes"
	stdpem "encoding/pem"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/pem"
)

// TestNew verifies the constructor returns a non-nil singleton with the
// canonical name.
func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"canonical name", "pem"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := pem.New()
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

// TestMarshal covers the encoder success path + the VALUE_INVALID branch.
func TestMarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      any
		wantErr string
	}
	tests := []tc{
		{"valid block round-trips", &stdpem.Block{Type: "TEST", Bytes: []byte("hello")}, ""},
		{"wrong type surfaces VALUE_INVALID", "nope", "VALUE_INVALID"},
		{"nil block surfaces VALUE_INVALID", (*stdpem.Block)(nil), "VALUE_INVALID"},
		//: Type=="JSON" + no headers is the promotion shape — Marshal takes
		//: the hand-written fast-path (marshalPromotionBlock).
		{"promotion-shape block uses fast-path", &stdpem.Block{Type: "JSON", Bytes: []byte("hello")}, ""},
		//: a header KEY containing a colon makes encoding/pem.Encode return
		//: a non-writer error (pem.go: "header key that contains a colon"),
		//: driving the MARSHAL_FAILED wrap. Headers != 0 so it bypasses the
		//: promotion fast-path and reaches stdpem.Encode.
		{"colon header key surfaces MARSHAL_FAILED", &stdpem.Block{Type: "CERT", Headers: map[string]string{"bad:key": "v"}, Bytes: []byte("x")}, "MARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := pem.New().Marshal(tc.in)
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

// TestUnmarshal covers the decoder success path plus VALUE_INVALID (wrong
// target) and UNMARSHAL_FAILED (no PEM block in input) branches.
func TestUnmarshal(t *testing.T) {
	t.Parallel()
	in := &stdpem.Block{Type: "TEST", Bytes: []byte("hello world")}
	encoded, merr := pem.New().Marshal(in)
	if merr != nil {
		t.Fatalf("Marshal setup err=%v", merr)
	}
	type tc struct {
		name    string
		data    []byte
		target  any
		wantErr string
	}
	var block *stdpem.Block
	var wrong string
	tests := []tc{
		{"round-trip success", encoded, &block, ""},
		{"wrong target surfaces VALUE_INVALID", []byte("x"), &wrong, "VALUE_INVALID"},
		{"no block surfaces UNMARSHAL_FAILED", []byte("not pem"), &block, "UNMARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := pem.New().Unmarshal(tc.data, tc.target)
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

// TestMarshalPromotionShape pins the promotion fast-path
// (marshalPromotionBlock + insertNewlinesEvery64): for a "JSON"-typed
// block with no headers, Marshal MUST emit bytes byte-identical to
// encoding/pem.EncodeToMemory (the production comment guarantees this),
// and the result MUST round-trip back to the same block. Payload sizes
// are chosen to hit every line-split shape: zero bytes, a single short
// line, an exact 48-byte input (one full 64-char base64 line), and a
// multi-line payload whose final line is partial.
func TestMarshalPromotionShape(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		payload []byte
	}
	tests := []tc{
		//: empty payload — bodyLen 0, lineCount 0 (loop body never runs).
		{"empty payload", []byte{}},
		//: short payload — one partial base64 line (< 64 chars).
		{"short single line", []byte("hello")},
		//: 48 input bytes encode to exactly 64 base64 chars = one full line.
		{"exact one full line", bytes.Repeat([]byte{0xAB}, 48)},
		//: 100 input bytes span multiple 64-char lines with a partial tail —
		//: forces the multi-iteration backwards walk in insertNewlinesEvery64.
		{"multi-line partial tail", bytes.Repeat([]byte{0x5A}, 100)},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		block := &stdpem.Block{Type: "JSON", Bytes: tc.payload}
		got, err := pem.New().Marshal(block)
		if err != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, err)
		}
		//: byte-identity contract — the fast-path must equal stdlib output.
		want := stdpem.EncodeToMemory(block)
		if !bytes.Equal(got, want) {
			t.Errorf("%s: promotion bytes diverge from pem.EncodeToMemory\n got=%q\nwant=%q", tc.name, got, want)
		}
		//: round-trip contract — decoding the fast-path bytes recovers the payload.
		var back *stdpem.Block
		if uerr := pem.New().Unmarshal(got, &back); uerr != nil {
			t.Fatalf("%s: Unmarshal err=%v", tc.name, uerr)
		}
		if back.Type != "JSON" || !bytes.Equal(back.Bytes, tc.payload) {
			t.Errorf("%s: round-trip mismatch: type=%q bytes=%q", tc.name, back.Type, back.Bytes)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
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
		{"format registered", func() bool { _, ok := codec.Lookup(codec.Format("pem")); return ok }},
		{"MIME resolved", func() bool { _, ok := codec.LookupMIME("application/x-pem-file"); return ok }},
		{"extension resolved", func() bool { _, ok := codec.LookupExt(".pem"); return ok }},
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
