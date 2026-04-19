package baseenc

import (
	"errors"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_wrapDecode covers both the success pass-through and the
// DECODE_FAILED wrapping branch.
func Test_wrapDecode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      []byte
		cause   error
		wantErr bool
	}
	tests := []tc{
		{"nil cause returns payload", []byte("ok"), nil, false},
		{"non-nil cause surfaces DECODE_FAILED", nil, errors.New("boom"), true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out, err := wrapDecode(tc.in, tc.cause)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
		if !tc.wantErr && string(out) != string(tc.in) {
			t.Errorf("%s: payload rewritten to %q", tc.name, out)
		}
		if tc.wantErr && !errs.HasReason(err, "DECODE_FAILED") {
			t.Errorf("%s: expected DECODE_FAILED, got %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_encodeASCII85 covers the buffered Ascii85 encoder helper.
func Test_encodeASCII85(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		raw      []byte
		wantNonE bool
	}
	tests := []tc{
		{"empty input encodes to empty", nil, false},
		{"non-empty input encodes", []byte("hi"), true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out, err := encodeASCII85(tc.raw)
		if err != nil {
			t.Fatalf("%s: encodeASCII85 err=%v", tc.name, err)
		}
		if tc.wantNonE && out == "" {
			t.Errorf("%s: got empty text for input %q", tc.name, tc.raw)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_decodeASCII85 covers the Ascii85 decode helper.
func Test_decodeASCII85(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		text    string
		wantErr bool
	}
	tests := []tc{
		{"empty text decodes to empty", "", false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := decodeASCII85(tc.text)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_wrapASCII85Encode confirms the helper emits ENCODE_FAILED for any
// non-nil cause.
func Test_wrapASCII85Encode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		cause error
	}
	tests := []tc{
		{"wraps a stdlib error", errors.New("io")},
		{"wraps a synthesized cause", errors.New(strings.Repeat("x", 3))},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := wrapASCII85Encode(tc.cause)
		if !errs.HasReason(err, "ENCODE_FAILED") {
			t.Errorf("%s: expected ENCODE_FAILED, got %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
