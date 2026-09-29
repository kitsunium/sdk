// Package queue_test — the SQL the broker sends, pinned as TEXT per dialect,
// and the table SQLMigration creates. The expectations are spelled out by
// hand: a test that rebuilt them with the renderer would keep exactly the bug
// it should catch.
package queue_test

import (
	"context"
	stdsql "database/sql"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	svcqueue "github.com/kitsunium/sdk/internal/service/queue"
)

// statementFor returns the text of the fixed statement playing role.
func statementFor(t *testing.T, rendered svcqueue.RenderedSQL, role string) string {
	t.Helper()
	for text, r := range rendered.Fixed {
		if r == role {
			return text
		}
	}
	t.Fatalf("no statement plays %s among %v", role, slices.Collect(maps.Values(rendered.Fixed)))
	return ""
}

// TestSQLTheStatementsAsSent pins every statement on PostgreSQL, and the ones
// whose grammar differs on MySQL and SQLite: the placeholders, the quoting,
// the lease's read that locks and skips — which SQLite has no clause for — and
// the write that takes SQLite's lock first.
func TestSQLTheStatementsAsSent(t *testing.T) {
	t.Parallel()
	type pinned struct {
		role, text string
		dialect    coresql.Dialect
	}
	for _, want := range []pinned{
		{dialect: coresql.DialectPostgres, role: "insert", text: `INSERT INTO "app__jobs" (id, dead, due, enqueued_at, deliveries, payload) VALUES ($1, 0, $2, $3, 0, $4)`},
		{dialect: coresql.DialectPostgres, role: "probe", text: `SELECT MIN(due) FROM "app__jobs" WHERE dead = 0`},
		{dialect: coresql.DialectPostgres, role: "pick", text: `SELECT id, enqueued_at, deliveries, lease, payload FROM "app__jobs" WHERE dead = 0 AND due <= $1` +
			` ORDER BY due, id LIMIT $2 FOR UPDATE SKIP LOCKED`},
		{dialect: coresql.DialectPostgres, role: "next", text: `SELECT MIN(due) FROM "app__jobs" WHERE dead = 0 AND due > $1`},
		{dialect: coresql.DialectPostgres, role: "ack", text: `DELETE FROM "app__jobs" WHERE id = $1 AND lease = $2 AND deliveries = $3 AND dead = 0 AND due > $4`},
		{dialect: coresql.DialectPostgres, role: "retry", text: `UPDATE "app__jobs" SET due = $1, lease = NULL WHERE id = $2 AND lease = $3 AND deliveries = $4` +
			` AND dead = 0 AND due > $5`},
		{dialect: coresql.DialectPostgres, role: "bury", text: `UPDATE "app__jobs" SET dead = 1, due = $1, lease = NULL, reason = $2, cause = $3, code = $4` +
			` WHERE id = $5 AND lease = $6 AND deliveries = $7 AND dead = 0 AND due > $8`},
		{dialect: coresql.DialectPostgres, role: "extend", text: `UPDATE "app__jobs" SET due = $1, lease = $2 WHERE id = $3 AND lease = $4 AND deliveries = $5` +
			` AND dead = 0 AND due > $6`},
		{dialect: coresql.DialectPostgres, role: "deadList", text: `SELECT id, enqueued_at, deliveries, due, reason, cause, code, payload FROM "app__jobs"` +
			` WHERE dead = 1 ORDER BY due, id LIMIT $1`},
		{dialect: coresql.DialectPostgres, role: "replay", text: `UPDATE "app__jobs" SET dead = 0, due = $1, deliveries = 0, reason = NULL, cause = NULL, code = NULL` +
			` WHERE id = $2 AND dead = 1`},
		{dialect: coresql.DialectPostgres, role: "deleteDead", text: `DELETE FROM "app__jobs" WHERE id = $1 AND dead = 1`},
		{dialect: coresql.DialectMySQL, role: "insert", text: "INSERT INTO `app__jobs` (id, dead, due, enqueued_at, deliveries, payload) VALUES (?, 0, ?, ?, 0, ?)"},
		{dialect: coresql.DialectMySQL, role: "pick", text: "SELECT id, enqueued_at, deliveries, lease, payload FROM `app__jobs` WHERE dead = 0 AND due <= ?" +
			" ORDER BY due, id LIMIT ? FOR UPDATE SKIP LOCKED"},
		{dialect: coresql.DialectMySQL, role: "ack", text: "DELETE FROM `app__jobs` WHERE id = ? AND lease = ? AND deliveries = ? AND dead = 0 AND due > ?"},
		{dialect: coresql.DialectSQLite, role: "lock", text: `DELETE FROM "app__jobs" WHERE 1 = 0`},
		{dialect: coresql.DialectSQLite, role: "pick", text: `SELECT id, enqueued_at, deliveries, lease, payload FROM "app__jobs" WHERE dead = 0 AND due <= ?` +
			` ORDER BY due, id LIMIT ?`},
		{dialect: coresql.DialectSQLite, role: "replay", text: `UPDATE "app__jobs" SET dead = 0, due = ?, deliveries = 0, reason = NULL, cause = NULL, code = NULL` +
			` WHERE id = ? AND dead = 1`},
	} {
		rendered := svcqueue.RenderSQLForTest(want.dialect, jobsTable)
		if got := statementFor(t, rendered, want.role); got != want.text {
			t.Errorf("%s %s =\n%s\nwant\n%s", want.dialect, want.role, got, want.text)
		}
	}
	for _, dialect := range []coresql.Dialect{coresql.DialectPostgres, coresql.DialectMySQL} {
		for text, role := range svcqueue.RenderSQLForTest(dialect, jobsTable).Fixed {
			if role == "lock" {
				t.Errorf("%s sends a lock statement %q; only SQLite's lock is a write", dialect, text)
			}
		}
	}
	postgres := svcqueue.RenderSQLForTest(coresql.DialectPostgres, jobsTable)
	if got, want := postgres.LeaseRows(2), `UPDATE "app__jobs" SET due = $1, lease = $2, deliveries = deliveries + 1`+
		` WHERE dead = 0 AND id IN ($3, $4)`; got != want {
		t.Errorf("the lease of two rows =\n%s\nwant\n%s", got, want)
	}
	if got, want := postgres.BuryRows(2), `UPDATE "app__jobs" SET dead = 1, due = $1, lease = NULL, reason = $2, cause = $3, code = $4`+
		` WHERE dead = 0 AND id IN ($5, $6)`; got != want {
		t.Errorf("the burial of two lapsed leases =\n%s\nwant\n%s", got, want)
	}
	mysql := svcqueue.RenderSQLForTest(coresql.DialectMySQL, jobsTable)
	if got, want := mysql.LeaseRows(3), "UPDATE `app__jobs` SET due = ?, lease = ?, deliveries = deliveries + 1"+
		" WHERE dead = 0 AND id IN (?, ?, ?)"; got != want {
		t.Errorf("the lease of three rows on MySQL =\n%s\nwant\n%s", got, want)
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

// TestSQLMigrationCreatesTheOneTable pins the DDL per dialect: the identifier
// and the lease binary, the instants 64-bit, the payload bytes that admit
// NULL, the UNIQUE (dead, due, id) index every read walks in order, the one
// statement doing nothing when the table exists — and a Down that drops it.
func TestSQLMigrationCreatesTheOneTable(t *testing.T) {
	t.Parallel()
	for dialect, want := range map[coresql.Dialect]string{
		coresql.DialectPostgres: `CREATE TABLE IF NOT EXISTS "app__jobs" (id bytea NOT NULL PRIMARY KEY, dead smallint NOT NULL,` +
			` due bigint NOT NULL, enqueued_at bigint NOT NULL, deliveries integer NOT NULL, lease bytea, payload bytea,` +
			` reason bytea, cause bytea, code bigint, UNIQUE (dead, due, id))`,
		coresql.DialectMySQL: "CREATE TABLE IF NOT EXISTS `app__jobs` (id VARBINARY(64) NOT NULL, dead TINYINT NOT NULL," +
			" due BIGINT NOT NULL, enqueued_at BIGINT NOT NULL, deliveries INT NOT NULL, lease VARBINARY(32) NULL," +
			" payload LONGBLOB NULL, reason VARBINARY(480) NULL, cause VARBINARY(480) NULL, code BIGINT NULL," +
			" PRIMARY KEY (id), UNIQUE KEY (dead, due, id)) ENGINE=InnoDB",
		coresql.DialectSQLite: `CREATE TABLE IF NOT EXISTS "app__jobs" (id BLOB NOT NULL PRIMARY KEY, dead INTEGER NOT NULL,` +
			` due INTEGER NOT NULL, enqueued_at INTEGER NOT NULL, deliveries INTEGER NOT NULL, lease BLOB, payload BLOB,` +
			` reason BLOB, cause BLOB, code INTEGER, UNIQUE (dead, due, id))`,
	} {
		migration, err := svcqueue.SQLMigration(dialect, jobsTable, 20260929120000)
		if err != nil {
			t.Fatalf("%s: SQLMigration() = %v", dialect, err)
		}
		if migration.Version != 20260929120000 || migration.Name != "queue app__jobs" {
			t.Fatalf("%s: migration %d %q", dialect, migration.Version, migration.Name)
		}
		up, down := &ddlRecorder{}, &ddlRecorder{}
		if err := migration.Up(t.Context(), up); err != nil {
			t.Fatalf("%s Up: %v", dialect, err)
		}
		if err := migration.Down(t.Context(), down); err != nil {
			t.Fatalf("%s Down: %v", dialect, err)
		}
		if !slices.Equal(up.sent, []string{want}) {
			t.Errorf("%s Up =\n%q\nwant\n%q", dialect, up.sent, want)
		}
		quote := `"`
		if dialect == coresql.DialectMySQL {
			quote = "`"
		}
		if wantDown := []string{"DROP TABLE IF EXISTS " + quote + "app__jobs" + quote}; !slices.Equal(down.sent, wantDown) {
			t.Errorf("%s Down = %q, want %q", dialect, down.sent, wantDown)
		}
		exportedUp, exportedDown := svcqueue.QueueTableSQLForTest(dialect, jobsTable)
		if !slices.Equal(exportedUp, up.sent) || !slices.Equal(exportedDown, down.sent) {
			t.Errorf("%s: the migration runs other statements than the ones rendered", dialect)
		}
	}
}

// TestSQLMigrationRefusals pins what SQLMigration refuses before it builds a
// value: a dialect it cannot spell, a table name it cannot interpolate, and a
// version the version table cannot hold.
func TestSQLMigrationRefusals(t *testing.T) {
	t.Parallel()
	if _, err := svcqueue.SQLMigration(coresql.DialectUnknown, jobsTable, 1); !errs.HasCode(err, svcqueue.CodeSQLQueueMisconfigured) {
		t.Errorf("SQLMigration(no dialect) = %v, want CodeSQLQueueMisconfigured", err)
	}
	if _, err := svcqueue.SQLMigration(coresql.DialectSQLite, "Jobs; DROP", 1); !errs.HasCode(err, svcqueue.CodeSQLQueueMisconfigured) {
		t.Errorf("SQLMigration(a table name that is not one) = %v, want CodeSQLQueueMisconfigured", err)
	}
	if _, err := svcqueue.SQLMigration(coresql.DialectSQLite, jobsTable, 0); !errs.HasCode(err, coresql.CodeInvalidMigration) {
		t.Errorf("SQLMigration(version 0) = %v, want CodeInvalidMigration", err)
	}
}
