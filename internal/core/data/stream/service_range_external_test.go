package stream_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/data/stream"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestTheSentinelsKeepTheirValues pins every sentinel of the domain to the
// value, reason and statuses ADR 0231 allocated: the core's 0.2.38.* and the
// engines' 0.3.94.*, declared here as ADR 0160 requires.
func TestTheSentinelsKeepTheirValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err    *errs.Error
		reason string
		code   errs.Code
		exit   int
		status int
	}{
		{stream.StreamMisconfigured, "STREAM_MISCONFIGURED", 0x00_02_26_01, 78, 500},
		{stream.RecordTooLarge, "RECORD_TOO_LARGE", 0x00_02_26_02, 78, 413},
		{stream.OffsetTrimmed, "OFFSET_TRIMMED", 0x00_02_26_03, 65, 409},
		{stream.OffsetAhead, "OFFSET_AHEAD", 0x00_02_26_04, 65, 409},
		{stream.UnknownPartition, "UNKNOWN_PARTITION", 0x00_02_26_05, 65, 400},
		{stream.InvalidBatchSize, "INVALID_BATCH_SIZE", 0x00_02_26_06, 78, 500},
		{stream.StreamFull, "STREAM_FULL", 0x00_02_26_07, 75, 503},
		{stream.UnknownGroup, "UNKNOWN_GROUP", 0x00_02_26_08, 66, 404},
		{stream.PartitionsMixed, "PARTITIONS_MIXED", 0x00_02_26_09, 65, 400},
		{stream.StreamBackendFailed, "STREAM_BACKEND_FAILED", 0x00_03_5E_01, 74, 500},
		{stream.StreamDirectoryUnusable, "STREAM_DIRECTORY_UNUSABLE", 0x00_03_5E_02, 78, 500},
		{stream.SQLStreamMisconfigured, "SQL_STREAM_MISCONFIGURED", 0x00_03_5E_03, 78, 500},
		{stream.SubscriberMisconfigured, "SUBSCRIBER_MISCONFIGURED", 0x00_03_5E_04, 78, 500},
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
