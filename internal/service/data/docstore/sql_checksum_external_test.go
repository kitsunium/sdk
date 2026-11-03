package docstore_test

import (
	"testing"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/service/data/docstore"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"
)

// TestSQLMigrationsDeclareTheChecksumOfWhatUpRuns pins #1056 on the store's
// two statement-built migrations: each declares a checksum, and it is the
// fingerprint of exactly the statements its Up sends, on every dialect — so
// a version table that recorded it refuses a build whose statements differ.
func TestSQLMigrationsDeclareTheChecksumOfWhatUpRuns(t *testing.T) {
	t.Parallel()
	builders := map[string]func(coresql.Dialect, string, uint64) (coresql.MigrationValue, error){
		"SQLMigration":         docstore.SQLMigration,
		"SQLVersionsMigration": docstore.SQLVersionsMigration,
	}
	for name, build := range builders {
		for _, dialect := range []coresql.Dialect{coresql.DialectPostgres, coresql.DialectMySQL, coresql.DialectSQLite} {
			migration, err := build(dialect, "members_accounts", 7)
			if err != nil {
				t.Fatalf("%s(%s) = %v", name, dialect, err)
			}
			var up ddlRecorder
			if err := migration.Up(t.Context(), &up); err != nil {
				t.Fatalf("%s(%s).Up = %v", name, dialect, err)
			}
			if want := svcsql.ChecksumOf(up.sent...); migration.Checksum == "" || migration.Checksum != want {
				t.Errorf("%s(%s).Checksum = %q, want the fingerprint of its Up, %q", name, dialect, migration.Checksum, want)
			}
		}
	}
	other, err := docstore.SQLMigration(coresql.DialectPostgres, "members_other", 7)
	if err != nil {
		t.Fatalf("SQLMigration(other) = %v", err)
	}
	same, err := docstore.SQLMigration(coresql.DialectPostgres, "members_accounts", 7)
	if err != nil {
		t.Fatalf("SQLMigration() = %v", err)
	}
	if other.Checksum == same.Checksum {
		t.Error("two stores' migrations carry one checksum: the table name is not in the fingerprint")
	}
}
