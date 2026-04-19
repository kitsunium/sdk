package pem_test

import (
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
	encoded, _ := pem.New().Marshal(in)
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
