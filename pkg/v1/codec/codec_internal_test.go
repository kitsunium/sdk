package codec

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_unknownFormat_Reason asserts unknownFormat produces a typed error
// carrying the UNKNOWN_FORMAT reason regardless of the Format value.
func Test_unknownFormat_Reason(t *testing.T) {
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
