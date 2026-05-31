package errs_test

import (
	"testing"

	errs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// stubError is a non-SDK error used to confirm TrailOf returns nil for errors
// that never passed through Define / Wrap.
type stubError struct {
	msg string
}

// Error renders the stored message, satisfying the error interface.
func (e stubError) Error() string {
	//: return the message verbatim.
	return e.msg
}

func Test_TrailOf(t *testing.T) {
	t.Parallel()

	origin := errs.Define(errs.Code(0x01_01_00_01), "ORIGIN", "origin failed", "origin detail")
	wrapped := errs.Wrap(origin, errs.WrapParams{
		Code:    errs.Code(0x01_01_00_02),
		Reason:  "OUTER",
		Public:  "outer view",
		Private: "outer view detail",
	})

	tests := []struct {
		name    string
		err     error
		wantNil bool
	}{
		{
			name:    "nil error has no trail",
			err:     nil,
			wantNil: true,
		},
		{
			name:    "plain stdlib error has no trail",
			err:     stubError{msg: "boom"},
			wantNil: true,
		},
		{
			name:    "wrapped chain carries the accumulated trail",
			err:     wrapped,
			wantNil: false,
		},
	}

	//: drive each fixture through the same trail expectations.
	for _, tc := range tests {
		//: parent is parallel, so each subtest is too.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := errs.TrailOf(tc.err)

			//: a non-SDK or nil error must yield a nil trail.
			if tc.wantNil {
				//: guard the nil expectation.
				if got != nil {
					t.Fatalf("TrailOf(%v) = %v, want nil", tc.err, got)
				}

				//: nil case fully checked.
				return
			}

			//: a wrapped SDK error must surface at least one trail frame.
			if len(got) == 0 {
				t.Fatalf("TrailOf wrapped = empty, want >= 1 frame")
			}
		})
	}
}
