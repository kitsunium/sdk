// Package docstore_test — the SQL the store sends, pinned as TEXT per dialect,
// and the tables SQLMigration creates. The expectations are spelled out by
// hand: a test that rebuilt them with the renderer would keep exactly the bug
// it should catch.
package docstore_test

import (
	"context"
	stdsql "database/sql"
	"errors"
	"maps"
	"slices"
	"testing"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	"github.com/kitsunium/sdk/internal/service/docstore"
)

// statementFor returns the text of the fixed statement playing role.
func statementFor(t *testing.T, rendered docstore.RenderedSQL, role string) string {
	t.Helper()
	for text, r := range rendered.Fixed {
		if r == role {
			return text
		}
	}
	t.Fatalf("no statement plays %s among %v", role, slices.Collect(maps.Values(rendered.Fixed)))
	return ""
}

// TestSQLTheDialectsDiverge pins, as text, the statements whose grammar
// differs between the engines: the upsert and its answer, the insertion over a
// taken key, Update's lock, and the read past a snapshot.
func TestSQLTheDialectsDiverge(t *testing.T) {
	t.Parallel()
	type pinned struct {
		role, text string
		dialect    coresql.Dialect
	}
	for _, want := range []pinned{
		{dialect: coresql.DialectPostgres, role: "upsert", text: `INSERT INTO "members__accounts" (doc_key, rev, doc) VALUES ($1, 1, $2) ` +
			`ON CONFLICT (doc_key) DO UPDATE SET rev = "members__accounts".rev + 1, doc = excluded.doc RETURNING rev`},
		{dialect: coresql.DialectPostgres, role: "insert", text: `INSERT INTO "members__accounts" (doc_key, rev, doc) VALUES ($1, 1, $2) ON CONFLICT (doc_key) DO NOTHING`},
		{dialect: coresql.DialectPostgres, role: "lockDoc", text: `SELECT doc FROM "members__accounts" WHERE doc_key = $1 FOR UPDATE`},
		{dialect: coresql.DialectPostgres, role: "writeLocked", text: `UPDATE "members__accounts" SET rev = rev + 1, doc = $1 WHERE doc_key = $2`},
		{dialect: coresql.DialectPostgres, role: "exists", text: `SELECT 1 FROM "members__accounts" WHERE doc_key = $1`},
		{dialect: coresql.DialectPostgres, role: "find", text: `SELECT d.doc FROM "members__accounts___ix" i JOIN "members__accounts" d ` +
			`ON d.doc_key = i.doc_key WHERE i.index_name = $1 AND i.index_key = $2 ORDER BY i.doc_key`},
		{dialect: coresql.DialectMySQL, role: "upsert", text: "INSERT INTO `members__accounts` (doc_key, rev, doc) VALUES (?, 1, ?) " +
			"ON DUPLICATE KEY UPDATE rev = rev + 1, doc = ?"},
		{dialect: coresql.DialectMySQL, role: "insert", text: "INSERT INTO `members__accounts` (doc_key, rev, doc) VALUES (?, 1, ?)"},
		{dialect: coresql.DialectMySQL, role: "lockDoc", text: "SELECT doc FROM `members__accounts` WHERE doc_key = ? FOR UPDATE"},
		{dialect: coresql.DialectMySQL, role: "writeLocked", text: "UPDATE `members__accounts` SET rev = rev + 1, doc = ? WHERE doc_key = ?"},
		{dialect: coresql.DialectMySQL, role: "exists", text: "SELECT 1 FROM `members__accounts` WHERE doc_key = ? LOCK IN SHARE MODE"},
		{dialect: coresql.DialectSQLite, role: "upsert", text: `INSERT INTO "members__accounts" (doc_key, rev, doc) VALUES (?, 1, ?) ` +
			`ON CONFLICT (doc_key) DO UPDATE SET rev = rev + 1, doc = excluded.doc RETURNING rev`},
		{dialect: coresql.DialectSQLite, role: "insert", text: `INSERT INTO "members__accounts" (doc_key, rev, doc) VALUES (?, 1, ?) ON CONFLICT (doc_key) DO NOTHING`},
		{dialect: coresql.DialectSQLite, role: "lockDoc", text: `UPDATE "members__accounts" SET rev = rev + 1 WHERE doc_key = ? RETURNING doc`},
		{dialect: coresql.DialectSQLite, role: "writeLocked", text: `UPDATE "members__accounts" SET doc = ? WHERE doc_key = ?`},
		{dialect: coresql.DialectSQLite, role: "exists", text: `SELECT 1 FROM "members__accounts" WHERE doc_key = ?`},
	} {
		rendered := docstore.RenderSQLForTest(want.dialect, accountsTable)
		if got := statementFor(t, rendered, want.role); got != want.text {
			t.Errorf("%s %s =\n%s\nwant\n%s", want.dialect, want.role, got, want.text)
		}
	}
	postgres := docstore.RenderSQLForTest(coresql.DialectPostgres, accountsTable)
	if got, want := postgres.UniqueTaken(2, false), `SELECT index_name FROM "members__accounts___ix" WHERE uniq = 1 AND doc_key <> $1`+
		` AND ((index_name = $2 AND index_key = $3) OR (index_name = $4 AND index_key = $5))`; got != want {
		t.Errorf("the unique check =\n%s\nwant\n%s", got, want)
	}
	if got, want := postgres.InsertIndexRows(2), `INSERT INTO "members__accounts___ix" (index_name, index_key, doc_key, uniq)`+
		` VALUES ($1, $2, $3, $4), ($5, $6, $7, $8)`; got != want {
		t.Errorf("the index rows' insertion =\n%s\nwant\n%s", got, want)
	}
	mysql := docstore.RenderSQLForTest(coresql.DialectMySQL, accountsTable)
	if got, want := mysql.UniqueTaken(1, true), "SELECT index_name FROM `members__accounts___ix` WHERE uniq = 1 AND doc_key <> ?"+
		" AND ((index_name = ? AND index_key = ?)) LOCK IN SHARE MODE"; got != want {
		t.Errorf("the unique check after a collision =\n%s\nwant\n%s", got, want)
	}
}

// ddlRecorder is an executor that records the statements a migration step
// sends and answers none of them.
type ddlRecorder struct{ sent []string }

// ExecContext records the statement.
func (r *ddlRecorder) ExecContext(_ context.Context, query string, _ ...any) (stdsql.Result, error) {
	r.sent = append(r.sent, query)
	return nil, nil
}

// QueryContext is not sent by a migration step.
func (r *ddlRecorder) QueryContext(context.Context, string, ...any) (*stdsql.Rows, error) {
	return nil, errors.New("a migration step queried")
}

// QueryRowContext is not sent by a migration step.
func (r *ddlRecorder) QueryRowContext(context.Context, string, ...any) *stdsql.Row { return nil }

// TestSQLMigrationCreatesBothTables pins the DDL per dialect: every key column
// binary, the document as bytes, the index table's three keys, each statement
// doing nothing when its table exists — and a Down that drops both.
func TestSQLMigrationCreatesBothTables(t *testing.T) {
	t.Parallel()
	for dialect, want := range map[coresql.Dialect][]string{
		coresql.DialectPostgres: {
			`CREATE TABLE IF NOT EXISTS "members__accounts" (doc_key bytea NOT NULL PRIMARY KEY, rev bigint NOT NULL, doc bytea NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS "members__accounts___ix" (index_name bytea NOT NULL, index_key bytea NOT NULL,` +
				` doc_key bytea NOT NULL, uniq smallint, PRIMARY KEY (index_name, index_key, doc_key),` +
				` UNIQUE (doc_key, index_name, index_key), UNIQUE (index_name, index_key, uniq))`,
		},
		coresql.DialectMySQL: {
			"CREATE TABLE IF NOT EXISTS `members__accounts` (doc_key VARBINARY(1024) NOT NULL, rev BIGINT NOT NULL," +
				" doc LONGBLOB NOT NULL, PRIMARY KEY (doc_key)) ENGINE=InnoDB",
			"CREATE TABLE IF NOT EXISTS `members__accounts___ix` (index_name VARBINARY(64) NOT NULL, index_key VARBINARY(1024) NOT NULL," +
				" doc_key VARBINARY(1024) NOT NULL, uniq TINYINT NULL, PRIMARY KEY (index_name, index_key, doc_key)," +
				" KEY (doc_key), UNIQUE KEY (index_name, index_key, uniq)) ENGINE=InnoDB",
		},
		coresql.DialectSQLite: {
			`CREATE TABLE IF NOT EXISTS "members__accounts" (doc_key BLOB NOT NULL PRIMARY KEY, rev INTEGER NOT NULL, doc BLOB NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS "members__accounts___ix" (index_name BLOB NOT NULL, index_key BLOB NOT NULL,` +
				` doc_key BLOB NOT NULL, uniq INTEGER, PRIMARY KEY (index_name, index_key, doc_key),` +
				` UNIQUE (doc_key, index_name, index_key), UNIQUE (index_name, index_key, uniq))`,
		},
	} {
		migration, err := docstore.SQLMigration(dialect, accountsTable, 20260927120000)
		must(t, err)
		if migration.Version != 20260927120000 || migration.Name != "docstore members__accounts" {
			t.Fatalf("%s: migration %d %q", dialect, migration.Version, migration.Name)
		}
		up, down := &ddlRecorder{}, &ddlRecorder{}
		must(t, migration.Up(t.Context(), up))
		must(t, migration.Down(t.Context(), down))
		if !slices.Equal(up.sent, want) {
			t.Errorf("%s Up =\n%q\nwant\n%q", dialect, up.sent, want)
		}
		quote := `"`
		if dialect == coresql.DialectMySQL {
			quote = "`"
		}
		wantDown := []string{
			"DROP TABLE IF EXISTS " + quote + "members__accounts___ix" + quote,
			"DROP TABLE IF EXISTS " + quote + "members__accounts" + quote,
		}
		if !slices.Equal(down.sent, wantDown) {
			t.Errorf("%s Down = %q, want %q", dialect, down.sent, wantDown)
		}
	}
}

// TestSQLMigrationRefusals pins what SQLMigration refuses before it builds a
// value: a dialect it cannot spell, a table name it cannot interpolate, and a
// version the version table cannot hold.
func TestSQLMigrationRefusals(t *testing.T) {
	t.Parallel()
	_, err := docstore.SQLMigration(coresql.DialectUnknown, accountsTable, 1)
	requireCode(t, err, docstore.CodeStoreMisconfigured, "no dialect")
	_, err = docstore.SQLMigration(coresql.DialectSQLite, "Members; DROP", 1)
	requireCode(t, err, docstore.CodeStoreMisconfigured, "a table name that is not one")
	_, err = docstore.SQLMigration(coresql.DialectSQLite, accountsTable, 0)
	requireCode(t, err, coresql.CodeInvalidMigration, "version zero")
}

// TestSQLTheVersionsStatements pins, as text per dialect, the statements a
// store that keeps versions adds: the claim a Put makes before it replaces a
// document, the reads that lock the version rows inside a write — locking
// reads on PostgreSQL and MySQL, which read past a REPEATABLE READ snapshot —
// the pruning, and the one read that answers every version with the document
// itself.
func TestSQLTheVersionsStatements(t *testing.T) {
	t.Parallel()
	type pinned struct {
		role, text string
		dialect    coresql.Dialect
	}
	for _, want := range []pinned{
		{dialect: coresql.DialectPostgres, role: "claim", text: `INSERT INTO "members__accounts" (doc_key, rev, doc) VALUES ($1, 1, $2) ` +
			`ON CONFLICT (doc_key) DO UPDATE SET rev = "members__accounts".rev + 1 RETURNING rev, doc`},
		{dialect: coresql.DialectMySQL, role: "claim", text: "INSERT INTO `members__accounts` (doc_key, rev, doc) VALUES (?, 1, ?) " +
			"ON DUPLICATE KEY UPDATE rev = rev + 1"},
		{dialect: coresql.DialectSQLite, role: "claim", text: `INSERT INTO "members__accounts" (doc_key, rev, doc) VALUES (?, 1, ?) ` +
			`ON CONFLICT (doc_key) DO UPDATE SET rev = rev + 1 RETURNING rev, doc`},
		{dialect: coresql.DialectPostgres, role: "versionHead", text: `SELECT num FROM "members__accounts___vs" WHERE doc_key = $1 AND doc IS NULL FOR UPDATE`},
		{dialect: coresql.DialectMySQL, role: "versionHead", text: "SELECT num FROM `members__accounts___vs` WHERE doc_key = ? AND doc IS NULL FOR UPDATE"},
		{dialect: coresql.DialectSQLite, role: "versionHead", text: `SELECT num FROM "members__accounts___vs" WHERE doc_key = ? AND doc IS NULL`},
		{dialect: coresql.DialectPostgres, role: "retireHead", text: `UPDATE "members__accounts___vs" SET doc = $1 WHERE doc_key = $2 AND num = $3`},
		{dialect: coresql.DialectPostgres, role: "pruneCut", text: `SELECT num FROM "members__accounts___vs" WHERE doc_key = $1 AND doc IS NOT NULL` +
			` ORDER BY num DESC LIMIT 1 OFFSET $2 FOR UPDATE`},
		{dialect: coresql.DialectMySQL, role: "pruneCut", text: "SELECT num FROM `members__accounts___vs` WHERE doc_key = ? AND doc IS NOT NULL" +
			" ORDER BY num DESC LIMIT 1 OFFSET ? FOR UPDATE"},
		{dialect: coresql.DialectSQLite, role: "pruneCut", text: `SELECT num FROM "members__accounts___vs" WHERE doc_key = ? AND doc IS NOT NULL` +
			` ORDER BY num DESC LIMIT 1 OFFSET ?`},
		{dialect: coresql.DialectPostgres, role: "pruneVersions", text: `DELETE FROM "members__accounts___vs" WHERE doc_key = $1 AND doc IS NOT NULL AND num <= $2`},
		{dialect: coresql.DialectPostgres, role: "dropVersions", text: `DELETE FROM "members__accounts___vs" WHERE doc_key = $1`},
		{dialect: coresql.DialectPostgres, role: "dropFormers", text: `DELETE FROM "members__accounts___vs" WHERE doc_key = $1 AND doc IS NOT NULL`},
		{dialect: coresql.DialectPostgres, role: "readVersions", text: `SELECT v.num, v.made_at, v.made_ns, v.meta, COALESCE(v.doc, d.doc) FROM "members__accounts" d` +
			` LEFT JOIN "members__accounts___vs" v ON v.doc_key = d.doc_key WHERE d.doc_key = $1 ORDER BY v.num DESC`},
		{dialect: coresql.DialectMySQL, role: "readVersions", text: "SELECT v.num, v.made_at, v.made_ns, v.meta, COALESCE(v.doc, d.doc) FROM `members__accounts` d" +
			" LEFT JOIN `members__accounts___vs` v ON v.doc_key = d.doc_key WHERE d.doc_key = ? ORDER BY v.num DESC"},
		{dialect: coresql.DialectMySQL, role: "lockFormers", text: "SELECT num, made_at, made_ns, meta, doc FROM `members__accounts___vs` WHERE doc_key = ?" +
			" AND doc IS NOT NULL ORDER BY num DESC FOR UPDATE"},
		{dialect: coresql.DialectSQLite, role: "lockFormers", text: `SELECT num, made_at, made_ns, meta, doc FROM "members__accounts___vs" WHERE doc_key = ?` +
			` AND doc IS NOT NULL ORDER BY num DESC`},
	} {
		rendered := docstore.RenderSQLForTest(want.dialect, accountsTable)
		if got := statementFor(t, rendered, want.role); got != want.text {
			t.Errorf("%s %s =\n%s\nwant\n%s", want.dialect, want.role, got, want.text)
		}
	}
	postgres := docstore.RenderSQLForTest(coresql.DialectPostgres, accountsTable)
	if got, want := postgres.InsertVersions(2), `INSERT INTO "members__accounts___vs" (doc_key, num, made_at, made_ns, meta, doc)`+
		` VALUES ($1, $2, $3, $4, $5, $6), ($7, $8, $9, $10, $11, $12)`; got != want {
		t.Errorf("the version rows' insertion =\n%s\nwant\n%s", got, want)
	}
}

// TestSQLVersionsMigrationCreatesTheVersionsTable pins the versions table's
// DDL per dialect — a binary key, the number, the instant, the metadata and
// the document, keyed by document and number — doing nothing when the table
// exists, and a Down that drops it.
func TestSQLVersionsMigrationCreatesTheVersionsTable(t *testing.T) {
	t.Parallel()
	for dialect, want := range map[coresql.Dialect]string{
		coresql.DialectPostgres: `CREATE TABLE IF NOT EXISTS "members__accounts___vs" (doc_key bytea NOT NULL, num bigint NOT NULL,` +
			` made_at bigint, made_ns integer, meta bytea, doc bytea, PRIMARY KEY (doc_key, num))`,
		coresql.DialectMySQL: "CREATE TABLE IF NOT EXISTS `members__accounts___vs` (doc_key VARBINARY(1024) NOT NULL, num BIGINT NOT NULL," +
			" made_at BIGINT NULL, made_ns INT NULL, meta LONGBLOB NULL, doc LONGBLOB NULL, PRIMARY KEY (doc_key, num)) ENGINE=InnoDB",
		coresql.DialectSQLite: `CREATE TABLE IF NOT EXISTS "members__accounts___vs" (doc_key BLOB NOT NULL, num INTEGER NOT NULL,` +
			` made_at INTEGER, made_ns INTEGER, meta BLOB, doc BLOB, PRIMARY KEY (doc_key, num))`,
	} {
		migration, err := docstore.SQLVersionsMigration(dialect, accountsTable, 20260928120000)
		must(t, err)
		if migration.Version != 20260928120000 || migration.Name != "docstore versions members__accounts" {
			t.Fatalf("%s: migration %d %q", dialect, migration.Version, migration.Name)
		}
		up, down := &ddlRecorder{}, &ddlRecorder{}
		must(t, migration.Up(t.Context(), up))
		must(t, migration.Down(t.Context(), down))
		if !slices.Equal(up.sent, []string{want}) {
			t.Errorf("%s Up =\n%q\nwant\n%q", dialect, up.sent, want)
		}
		quote := `"`
		if dialect == coresql.DialectMySQL {
			quote = "`"
		}
		if wantDown := []string{"DROP TABLE IF EXISTS " + quote + "members__accounts___vs" + quote}; !slices.Equal(down.sent, wantDown) {
			t.Errorf("%s Down = %q, want %q", dialect, down.sent, wantDown)
		}
	}
	_, err := docstore.SQLVersionsMigration(coresql.DialectUnknown, accountsTable, 1)
	requireCode(t, err, docstore.CodeStoreMisconfigured, "no dialect")
	_, err = docstore.SQLVersionsMigration(coresql.DialectSQLite, "Members; DROP", 1)
	requireCode(t, err, docstore.CodeStoreMisconfigured, "a table name that is not one")
	_, err = docstore.SQLVersionsMigration(coresql.DialectSQLite, accountsTable, 0)
	requireCode(t, err, coresql.CodeInvalidMigration, "version zero")
}
