package sql_test

import (
	"testing"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestTheServiceRangeKeepsItsValues pins the 0.3.54.* sentinels the transaction
// manager, the health probe and the migration runner emit to the values,
// reasons and statuses they carried while internal/service/data/sql declared
// them. ADR 0160 moved the declarations into this package and changed none of
// them: a consumer branching on a code, an alert matching a reason and a
// supervisor reading an exit status all see what they saw before.
func TestTheServiceRangeKeepsItsValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err    *errs.Error
		reason string
		code   errs.Code
		exit   int
		status int
	}{
		{coresql.ConfigInvalid, "CONFIG_INVALID", 0x00_03_36_01, 78, 500},
		{coresql.PoolMisconfigured, "POOL_MISCONFIGURED", 0x00_03_36_02, 78, 500},
		{coresql.BeginFailed, "BEGIN_FAILED", 0x00_03_36_03, 75, 503},
		{coresql.CommitFailed, "COMMIT_FAILED", 0x00_03_36_04, 75, 503},
		{coresql.RollbackFailed, "ROLLBACK_FAILED", 0x00_03_36_05, 75, 500},
		{coresql.SavepointFailed, "SAVEPOINT_FAILED", 0x00_03_36_06, 75, 500},
		{coresql.TxPoisoned, "TX_POISONED", 0x00_03_36_07, 75, 500},
		{coresql.TxClosed, "TX_CLOSED", 0x00_03_36_08, 78, 500},
		{coresql.HealthCheckFailed, "HEALTH_CHECK_FAILED", 0x00_03_36_09, 75, 503},
		{coresql.HealthCheckTimeout, "HEALTH_CHECK_TIMEOUT", 0x00_03_36_0A, 75, 503},
		{coresql.MigrationFailed, "MIGRATION_FAILED", 0x00_03_36_0B, 75, 500},
		{coresql.MigrationOutOfOrder, "MIGRATION_OUT_OF_ORDER", 0x00_03_36_0C, 78, 500},
		{coresql.MigrationLockUnsupported, "MIGRATION_LOCK_UNSUPPORTED", 0x00_03_36_0D, 78, 500},
		{coresql.MigrationLockTimeout, "MIGRATION_LOCK_TIMEOUT", 0x00_03_36_0E, 75, 500},
		{coresql.MigrationUnknownVersion, "MIGRATION_UNKNOWN_VERSION", 0x00_03_36_0F, 78, 500},
		{coresql.VersionTableInvalid, "VERSION_TABLE_INVALID", 0x00_03_36_10, 78, 500},
		{coresql.DuplicateMigration, "DUPLICATE_MIGRATION", 0x00_03_36_11, 78, 500},
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
