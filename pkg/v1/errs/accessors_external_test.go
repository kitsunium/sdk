package errs_test

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
)

func TestV1ErrsOnStdlibError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"stdlib error yields defaults and false flags"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := errors.New("plain stdlib")
			if _, ok := errs.CodeOf(err); ok {
				t.Error("CodeOf should return ok=false for stdlib error")
			}
			if errs.HasCode(err, 0x01_01_00_01) {
				t.Error("HasCode should be false for stdlib error")
			}
			if errs.HTTPStatusOf(err) != 500 {
				t.Errorf("HTTPStatusOf = %d", errs.HTTPStatusOf(err))
			}
		})
	}
}

// TestFieldsOfWithoutAnSDKError pins the absence arm through the facade: no
// *errs.Error on the chain, no fields — nil, not an empty slice.
func TestFieldsOfWithoutAnSDKError(t *testing.T) {
	t.Parallel()
	for _, err := range []error{nil, errors.New("plain")} {
		if got := errs.FieldsOf(err); got != nil {
			t.Errorf("FieldsOf(%v) = %v, want nil", err, got)
		}
	}
}
