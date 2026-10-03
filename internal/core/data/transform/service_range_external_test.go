package transform_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestTheServiceRangeKeepsItsValues pins the 0.3.26.* sentinels the stdlib
// schemes wrap a compress/* failure in to the values, reasons and statuses they
// carried while internal/service/data/transform declared them. ADR 0160 moved
// the declarations into this package and changed none of them: a consumer
// branching on a code, an alert matching a reason and a supervisor reading an
// exit status all see what they saw before.
func TestTheServiceRangeKeepsItsValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err    *errs.Error
		reason string
		code   errs.Code
		exit   int
		status int
	}{
		{transform.GzipFailed, "GZIP_FAILED", 0x00_03_1A_01, 70, 500},
		{transform.FlateFailed, "FLATE_FAILED", 0x00_03_1A_02, 70, 500},
		{transform.ZlibFailed, "ZLIB_FAILED", 0x00_03_1A_03, 70, 500},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			t.Parallel()
			if code, ok := errs.CodeOf(tc.err); !ok || code != tc.code {
				t.Errorf("CodeOf = (%v, %t), want (%v, true)", code, ok, tc.code)
			}
			if !errs.HasReason(tc.err, tc.reason) {
				t.Errorf("reason is not %q", tc.reason)
			}
			if got := errs.ExitCodeOf(tc.err); got != tc.exit {
				t.Errorf("ExitCodeOf = %d, want %d", got, tc.exit)
			}
			if got := errs.HTTPStatusOf(tc.err); got != tc.status {
				t.Errorf("HTTPStatusOf = %d, want %d", got, tc.status)
			}
		})
	}
}
