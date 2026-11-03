package sqlite_test

import (
	stdsql "database/sql"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// compatSet is the migration set of an application keeping a document store
// and a queue beside a table of its own, as this build declares it — or, with
// stripped, as an earlier release did, before any declared a checksum.
func compatSet(t *testing.T, stripped bool) []sql.Migration {
	t.Helper()
	docs, err := docstore.SQLMigration(sql.DialectSQLite, "accounts", 1)
	if err != nil {
		t.Fatalf("docstore.SQLMigration: %v", err)
	}
	versions, err := docstore.SQLVersionsMigration(sql.DialectSQLite, "accounts", 2)
	if err != nil {
		t.Fatalf("docstore.SQLVersionsMigration: %v", err)
	}
	jobs, err := queue.SQLMigration(sql.DialectSQLite, "jobs", 3)
	if err != nil {
		t.Fatalf("queue.SQLMigration: %v", err)
	}
	own := sql.Migration{
		Version: 4, Name: "own", Up: sql.Statements("CREATE TABLE own (id INTEGER)"), Down: sql.Statements("DROP TABLE own"),
	}
	set := []sql.Migration{docs, versions, jobs, own}
	//: the earlier release's values: the same statements, no fingerprint.
	if stripped {
		for i := range set {
			set[i].Checksum = ""
		}
	}
	return set
}

// migrateUp runs set over db's schema_versions table.
func migrateUp(t *testing.T, db *stdsql.DB, set []sql.Migration) error {
	t.Helper()
	runner, err := sql.NewMigrator(sql.Config{DB: db, Dialect: sql.DialectSQLite, Pool: sql.PoolConfig{MaxOpen: 1}},
		sql.MigrateConfig{Migrations: set, VersionTable: "schema_versions"})
	if err != nil {
		t.Fatalf("NewMigrator: %v", err)
	}
	return runner.Up(t.Context())
}

// TestAnInstallFromBeforeChecksumsUpgradesInPlace pins #1056's upgrade path
// on a real SQLite file: an install an earlier release migrated — a
// three-column version table, the store's and the queue's migrations recorded
// without a checksum — runs this build's set, where those three declare one.
// The table gains its column, the three versions are given the fingerprints
// the migrations declare and nothing is applied again; the application's own
// migration, which declares none, is never compared; a second run verifies;
// and only an edited statement is refused.
func TestAnInstallFromBeforeChecksumsUpgradesInPlace(t *testing.T) {
	db, err := stdsql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "app.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := migrateUp(t, db, compatSet(t, true)); err != nil {
		t.Fatalf("the earlier release's Up: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), "SELECT checksum FROM schema_versions WHERE 1 = 0"); err == nil {
		t.Fatal("the earlier release's version table has a checksum column; the upgrade below proves nothing")
	}
	current := compatSet(t, false)
	if err := migrateUp(t, db, current); err != nil {
		t.Fatalf("this build's Up over the earlier install: %v", err)
	}
	rows, err := db.QueryContext(t.Context(), "SELECT version, checksum FROM schema_versions ORDER BY version")
	if err != nil {
		t.Fatalf("read the version table: %v", err)
	}
	recorded := map[uint64]stdsql.NullString{}
	for rows.Next() {
		var version uint64
		var sum stdsql.NullString
		if err := rows.Scan(&version, &sum); err != nil {
			t.Fatalf("scan: %v", err)
		}
		recorded[version] = sum
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if len(recorded) != len(current) {
		t.Fatalf("the version table holds %d versions after the upgrade, want %d: something ran twice or vanished", len(recorded), len(current))
	}
	for _, migration := range current {
		sum := recorded[migration.Version]
		if migration.Checksum == "" {
			if sum.Valid {
				t.Errorf("version %d declares no checksum and was given %q", migration.Version, sum.String)
			}
			continue
		}
		if !sum.Valid || sum.String != migration.Checksum {
			t.Errorf("version %d recorded %v, want its declared %q", migration.Version, sum, migration.Checksum)
		}
	}
	if err := migrateUp(t, db, compatSet(t, false)); err != nil {
		t.Fatalf("a second run of the same build: %v", err)
	}
	edited := compatSet(t, false)
	edited[2].Checksum = sql.ChecksumOf("CREATE TABLE jobs (id TEXT)")
	if err := migrateUp(t, db, edited); !errs.HasCode(err, sql.MigrationChanged.Code()) {
		t.Fatalf("Up() with the queue's statements edited = %v, want MIGRATION_CHANGED", err)
	}
}
