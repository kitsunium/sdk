package mysql

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// errInnerBoom is a generic cause used to exercise the wrap helpers' errors.Is
// transparency and the sink wrapper's error-precedence path.
var errInnerBoom = errors.New("inner boom")

func Test_wrapClientInit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		cause error
	}{
		{"nil cause carries the init code", nil},
		{"non-nil cause stays Is-matchable", errInnerBoom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := wrapClientInit(tc.cause)
			//: every wrap must carry the client-init code.
			if !errs.HasCode(err, CodeMySQLClientInitFailed) {
				t.Errorf("%s: err=%v want client-init", tc.name, err)
			}
			//: a non-nil cause must remain reachable via errors.Is.
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Errorf("%s: errors.Is lost the cause", tc.name)
			}
		})
	}
}

func Test_wrapInsert(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		cause error
	}{
		{"nil cause carries the insert code", nil},
		{"non-nil cause stays Is-matchable", errInnerBoom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := wrapInsert(tc.cause, 3)
			//: every wrap must carry the insert code.
			if !errs.HasCode(err, CodeMySQLInsertFailed) {
				t.Errorf("%s: err=%v want insert", tc.name, err)
			}
			//: a non-nil cause must remain reachable via errors.Is.
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Errorf("%s: errors.Is lost the cause", tc.name)
			}
		})
	}
}

func Test_exitIOErr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want int
	}{
		{"sysexits EX_IOERR is 74", 74},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: the I/O exit code must match the sysexits constant.
			if exitIOErr != tc.want {
				t.Errorf("%s: exitIOErr = %d, want %d", tc.name, exitIOErr, tc.want)
			}
		})
	}
}
