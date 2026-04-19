package baseenc

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_wrapDecode_ReasonPassthrough covers both branches of wrapDecode:
// nil error passes the payload through unchanged; non-nil error is wrapped
// with the DECODE_FAILED reason.
func Test_wrapDecode_ReasonPassthrough(t *testing.T) {
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
