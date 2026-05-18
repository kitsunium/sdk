package asn1_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/asn1"
)

type asn1Payload struct {
	N int    `json:"n"`
	S string `json:"s"`
}

// TestMarshal covers the asn1 codec's Marshal path including the
// MARSHAL_FAILED error case on unsupported types.
func TestMarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      any
		wantErr string
	}
	tests := []tc{
		{"struct payload", asn1Payload{N: 42, S: "x"}, ""},
		{"channel triggers MARSHAL_FAILED", make(chan int), "MARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := asn1.New().Marshal(tc.in)
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

// TestUnmarshal covers the asn1 codec's Unmarshal path including the
// UNMARSHAL_FAILED branch on malformed bytes.
func TestUnmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		wantErr bool
	}
	good, gerr := asn1.New().Marshal(asn1Payload{N: 1, S: "a"})
	if gerr != nil {
		t.Fatalf("seed Marshal err=%v", gerr)
	}
	tests := []tc{
		{"round-trip success", good, false},
		{"malformed bytes surface UNMARSHAL_FAILED", []byte{0xff, 0x00}, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var out asn1Payload
		err := asn1.New().Unmarshal(tc.data, &out)
		if tc.wantErr != (err != nil) {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
		if tc.wantErr && !errs.HasReason(err, "UNMARSHAL_FAILED") {
			t.Errorf("%s: expected UNMARSHAL_FAILED, got %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestNew verifies the public constructor returns a non-nil singleton with
// the canonical name advertised.
func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"canonical name", "asn1-der"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := asn1.New()
		if c == nil {
			t.Fatalf("%s: New() returned nil", tc.name)
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

// TestRegisteredViaImport asserts the codec self-registers under its Name,
// MIME, and primary extension when the package is imported.
func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		check func() bool
	}
	tests := []tc{
		{"format registered", func() bool {
			_, ok := codec.Lookup(codec.Format("asn1-der"))
			return ok
		}},
		{"MIME resolved", func() bool {
			_, ok := codec.LookupMIME("application/pkix-cert")
			return ok
		}},
		{"extension resolved", func() bool {
			_, ok := codec.LookupExt(".der")
			return ok
		}},
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
