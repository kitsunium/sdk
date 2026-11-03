package sql

import (
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
)

// migratePlan is the validated, sorted, clamped form of a MigrateConfig.
type migratePlan struct {
	// migrations is the runner's own copy, sorted by ascending version.
	migrations []coresql.MigrationValue
	// table is the validated version-table identifier.
	table string
	// lockBudget bounds the wait for the advisory lock.
	lockBudget time.Duration
	// retry is the interval between acquisition attempts.
	retry time.Duration
	// checksums reports whether any migration of the set declares a
	// checksum: only then does the runner read, write and add the version
	// table's checksum column, so a set that declares none sends exactly the
	// statements it always sent.
	checksums bool
}
