package queue_test

import (
	"testing"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"
)

// TestSQLMigrationDeclaresTheChecksumOfWhatUpRuns pins #1056 on the queue's
// table: SQLMigration declares a checksum, the fingerprint of exactly the
// statement its Up sends, on every dialect.
func TestSQLMigrationDeclaresTheChecksumOfWhatUpRuns(t *testing.T) {
	t.Parallel()
	for _, dialect := range []coresql.Dialect{coresql.DialectPostgres, coresql.DialectMySQL, coresql.DialectSQLite} {
		migration, err := svcqueue.SQLMigration(dialect, "jobs", 3)
		if err != nil {
			t.Fatalf("SQLMigration(%s) = %v", dialect, err)
		}
		var up ddlRecorder
		if err := migration.Up(t.Context(), &up); err != nil {
			t.Fatalf("SQLMigration(%s).Up = %v", dialect, err)
		}
		if want := svcsql.ChecksumOf(up.sent...); migration.Checksum == "" || migration.Checksum != want {
			t.Errorf("SQLMigration(%s).Checksum = %q, want the fingerprint of its Up, %q", dialect, migration.Checksum, want)
		}
	}
}
