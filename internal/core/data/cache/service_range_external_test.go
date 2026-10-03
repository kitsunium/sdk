package cache_test

import (
	"testing"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestTheServiceRangeKeepsItsValues pins the 0.3.48.* sentinels the chained
// store emits to the values, reasons and statuses they carried while
// internal/service/data/cache declared them. ADR 0160 moved the declarations
// into this package and changed none of them: a consumer branching on a code,
// an alert matching a reason and a supervisor reading an exit status all see
// what they saw before.
func TestTheServiceRangeKeepsItsValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err    *errs.Error
		reason string
		code   errs.Code
		exit   int
		status int
	}{
		{corecache.CacheChainMisconfigured, "CACHE_CHAIN_MISCONFIGURED", 0x00_03_30_01, 78, 500},
		{corecache.CacheTierFailed, "CACHE_TIER_FAILED", 0x00_03_30_02, 70, 500},
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
