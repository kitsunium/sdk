package codec

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_unknownFormat covers the facade-level UNKNOWN_FORMAT error builder.
func Test_unknownFormat(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   Format
	}
	tests := []tc{
		{"empty format", ""},
		{"unregistered format", Format("zzz-absent")},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := unknownFormat(tc.in)
		if err == nil {
			t.Fatalf("%s: unknownFormat returned nil", tc.name)
		}
		if !errs.HasReason(err, "UNKNOWN_FORMAT") {
			t.Errorf("%s: expected UNKNOWN_FORMAT, got %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_resolveStreaming covers the lookup + type-assertion helper feeding
// NewEncoder / NewDecoder.
func Test_resolveStreaming(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      Format
		wantOK  bool
		wantRsn string // empty if wantOK
	}
	tests := []tc{
		{"streaming codec resolves", JSON, true, ""},
		{"non-streaming codec rejected", ASN1DER, false, "STREAMING_UNSUPPORTED"},
		{"unknown format rejected", Format("absent-zzz"), false, "UNKNOWN_FORMAT"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		sc, err := resolveStreaming(tc.in)
		if tc.wantOK {
			if err != nil {
				t.Fatalf("%s: unexpected err=%v", tc.name, err)
			}
			if sc == nil {
				t.Errorf("%s: nil StreamingCodec", tc.name)
			}
			return
		}
		if err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
		if tc.wantRsn != "" && !errs.HasReason(err, tc.wantRsn) {
			t.Errorf("%s: expected reason %q, got %v", tc.name, tc.wantRsn, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
