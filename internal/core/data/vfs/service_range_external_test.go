package vfs_test

import (
	"testing"

	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestTheServiceRangeKeepsItsValues pins the 0.3.55.* sentinels the concrete
// filesystem emits to the values, reasons and statuses they carried while
// internal/service/data/vfs declared them. ADR 0160 moved the declarations into
// this package and changed none of them: a consumer branching on a code, an
// alert matching a reason and a supervisor reading an exit status all see what
// they saw before.
func TestTheServiceRangeKeepsItsValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err    *kerrs.Error
		reason string
		code   kerrs.Code
		exit   int
		status int
	}{
		{corevfs.RootUnavailable, "ROOT_UNAVAILABLE", 0x00_03_37_01, 78, 500},
		{corevfs.DirectorySyncFailed, "DIRECTORY_SYNC_FAILED", 0x00_03_37_02, 74, 500},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			t.Parallel()
			if code, ok := kerrs.CodeOf(tc.err); !ok || code != tc.code {
				t.Errorf("CodeOf = (%v, %t), want (%v, true)", code, ok, tc.code)
			}
			if !kerrs.HasReason(tc.err, tc.reason) {
				t.Errorf("reason is not %q", tc.reason)
			}
			if got := kerrs.ExitCodeOf(tc.err); got != tc.exit {
				t.Errorf("ExitCodeOf = %d, want %d", got, tc.exit)
			}
			if got := kerrs.HTTPStatusOf(tc.err); got != tc.status {
				t.Errorf("HTTPStatusOf = %d, want %d", got, tc.status)
			}
		})
	}
}
