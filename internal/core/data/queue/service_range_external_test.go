package queue_test

import (
	"testing"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestTheServiceRangeKeepsItsValues pins the 0.3.53.* sentinels the brokers and
// the consumer engine emit to the values, reasons and statuses they carried
// while internal/service/data/queue declared them. ADR 0160 moved the
// declarations into this package and changed none of them: a consumer branching
// on a code, an alert matching a reason and a supervisor reading an exit status
// all see what they saw before.
func TestTheServiceRangeKeepsItsValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err    *errs.Error
		reason string
		code   errs.Code
		exit   int
		status int
	}{
		{corequeue.QueueBackendFailed, "QUEUE_BACKEND_FAILED", 0x00_03_35_01, 74, 500},
		{corequeue.QueueDirectoryUnusable, "QUEUE_DIRECTORY_UNUSABLE", 0x00_03_35_02, 78, 500},
		{corequeue.ConsumerMisconfigured, "CONSUMER_MISCONFIGURED", 0x00_03_35_03, 78, 500},
		{corequeue.HandlerPanicked, "HANDLER_PANICKED", 0x00_03_35_04, 70, 500},
		{corequeue.SQLQueueMisconfigured, "SQL_QUEUE_MISCONFIGURED", 0x00_03_35_05, 78, 500},
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
