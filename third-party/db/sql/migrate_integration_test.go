//go:build integration

// Package sql_test — the migration runner on each real engine, and SQLite's
// lock proven to be the database file's write lock (ADR 0140).
package sql_test

import (
	"context"
	stdsql "database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// migrator returns the SDK's runner over e with its own version table.
func migrator(t *testing.T, e *engine, versions string, budget time.Duration, migrations ...sql.Migration) sql.Migrator {
	t.Helper()
	runner, err := sql.NewMigrator(sql.Config{DB: e.db, Dialect: e.dialect, Pool: sql.PoolConfig{MaxOpen: 16}},
		sql.MigrateConfig{Migrations: migrations, VersionTable: versions, LockTimeout: budget, LockRetryInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewMigrator: %v", err)
	}
	return runner
}

// countRows counts the rows of table on e.
func countRows(t *testing.T, e *engine, table string) int {
	t.Helper()
	var n int
	must(t, e.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&n))
	return n
}

// TestTwoRunnersApplyAMigrationOnce is the two-instances case on every
// engine: two runners started together apply the set once — the second waits
// for the first and finds nothing pending. SQLite runs twice: its default
// deferred BEGIN, where the runner's own statement takes the lock, and
// _txlock=immediate, where BEGIN takes it and answers busy itself.
func TestTwoRunnersApplyAMigrationOnce(t *testing.T) {
	t.Parallel()
	for name, open := range map[string]func(testing.TB) *engine{
		"sqlite":           func(tb testing.TB) *engine { return sqliteEngine(tb, "") },
		"sqlite-immediate": func(tb testing.TB) *engine { return sqliteEngine(tb, "immediate") },
		"postgres":         postgresEngine,
		"mysql":            mysqlEngine,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			twoRunners(t, open(t))
		})
	}
}

// twoRunners starts two runners of one migration set together on e.
func twoRunners(t *testing.T, e *engine) {
	t.Helper()
	versions, ledger := uniqueName("versions_"), uniqueName("ledger_")
	migrations := []sql.Migration{
		{
			Version: 1, Name: "ledger", Up: sql.Statements("CREATE TABLE " + ledger + " (n INT NOT NULL)"),
			Down: sql.Statements("DROP TABLE " + ledger),
		},
		{
			Version: 2, Name: "one row per application", Up: sql.Statements("INSERT INTO " + ledger + " (n) VALUES (1)"),
			Down: sql.Statements("DELETE FROM " + ledger),
		},
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if err := migrator(t, e, versions, time.Minute, migrations...).Up(t.Context()); err != nil {
				t.Errorf("Up() = %v", err)
			}
		})
	}
	wg.Wait()
	if n := countRows(t, e, ledger); n != 1 {
		t.Fatalf("the migration ran %d times, want once", n)
	}
	pending, err := migrator(t, e, versions, time.Minute, migrations...).Plan(t.Context())
	if err != nil || len(pending) != 0 {
		t.Fatalf("Plan() after both runs = %v, %v", pending, err)
	}
	must(t, migrator(t, e, versions, time.Minute, migrations...).Down(t.Context(), 1))
	if n := countRows(t, e, ledger); n != 0 {
		t.Fatalf("Down(1) left %d rows", n)
	}
}

// TestTheSQLiteRunHoldsTheFilesWriteLock proves what ADR 0140 rests on: while
// a SQLite run is inside a migration, the database file's write lock is held
// — another connection's BEGIN IMMEDIATE is refused busy, and a second runner
// waits out its budget and applies nothing — and the lock is gone with the run.
func TestTheSQLiteRunHoldsTheFilesWriteLock(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "locked.sqlite")
	e := &engine{db: openSQLite(t, path, "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"), name: "sqlite", dialect: sql.DialectSQLite}
	impatient := openSQLite(t, path, "")
	versions := uniqueName("versions_")
	inside, release := make(chan struct{}), make(chan struct{})
	holding := sql.Migration{
		Version: 1, Name: "holds the run", Down: sql.Irreversible,
		Up: func(ctx context.Context, ex sql.Executor) error {
			if _, err := ex.ExecContext(ctx, "CREATE TABLE held (n INT)"); err != nil {
				return err
			}
			close(inside)
			<-release
			return nil
		},
	}
	first := make(chan error, 1)
	var running sync.WaitGroup
	running.Go(func() { first <- migrator(t, e, versions, time.Minute, holding).Up(context.Background()) })
	t.Cleanup(running.Wait)
	//: registered after the wait, so it runs first: a failing assertion
	//: must not leave the run parked inside its migration.
	letGo := sync.OnceFunc(func() { close(release) })
	t.Cleanup(letGo)
	<-inside
	conn, err := impatient.Conn(t.Context())
	must(t, err)
	_, err = conn.ExecContext(t.Context(), "BEGIN IMMEDIATE")
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Errorf("BEGIN IMMEDIATE beside a run = %v, want SQLite's busy answer", err)
	}
	must(t, conn.Close())
	//: on a pool with no busy timeout, so each attempt answers at once and the
	//: budget is the runner's; with one, an attempt waits that long first.
	second := migrator(t, &engine{db: impatient, name: "sqlite", dialect: sql.DialectSQLite}, versions, 300*time.Millisecond, holding)
	if err := second.Up(t.Context()); !errs.HasCode(err, sql.MigrationLockTimeout.Code()) {
		t.Errorf("a second runner beside the first = %v, want MIGRATION_LOCK_TIMEOUT", err)
	}
	letGo()
	must(t, <-first)
	if pending, planErr := second.Plan(t.Context()); planErr != nil || len(pending) != 0 {
		t.Fatalf("Plan() after the run = %v, %v", pending, planErr)
	}
	tx, err := impatient.BeginTx(t.Context(), nil)
	must(t, err)
	_, err = tx.ExecContext(t.Context(), "INSERT INTO held (n) VALUES (1)")
	must(t, errors.Join(err, tx.Commit()))
}

// TestASQLiteMigrationThatFailsKeepsTheOnesBeforeIt pins, on the real engine,
// that each migration of a SQLite run is still atomic on its own: the failing
// one leaves nothing, the ones before it are committed, and the run stops.
func TestASQLiteMigrationThatFailsKeepsTheOnesBeforeIt(t *testing.T) {
	t.Parallel()
	e := sqliteEngine(t, "")
	versions := uniqueName("versions_")
	runner := migrator(t, e, versions, time.Minute,
		sql.Migration{Version: 1, Name: "kept", Up: sql.Statements("CREATE TABLE kept (n INT)"), Down: sql.Statements("DROP TABLE kept")},
		sql.Migration{
			Version: 2, Name: "fails halfway", Down: sql.Irreversible,
			Up: sql.Statements("CREATE TABLE undone (n INT)", "THIS IS NOT SQL"),
		},
		sql.Migration{Version: 3, Name: "never reached", Up: sql.Statements("CREATE TABLE never (n INT)"), Down: sql.Irreversible},
	)
	err := runner.Up(t.Context())
	if !errs.HasCode(err, sql.MigrationFailed.Code()) {
		t.Fatalf("Up() = %v, want MIGRATION_FAILED", err)
	}
	if n := countRows(t, e, "kept"); n != 0 {
		t.Fatalf("kept holds %d rows", n)
	}
	for _, table := range []string{"undone", "never"} {
		var name string
		err := e.db.QueryRowContext(t.Context(), "SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&name)
		if !errors.Is(err, stdsql.ErrNoRows) {
			t.Fatalf("table %s exists after the run stopped: %v", table, err)
		}
	}
	if n := countRows(t, e, versions); n != 1 {
		t.Fatalf("the version table records %d migrations, want the one that applied", n)
	}
}

// openSQLite opens a pool onto the SQLite file at path with the DSN suffix.
func openSQLite(t *testing.T, path, suffix string) *stdsql.DB {
	t.Helper()
	db, err := stdsql.Open("sqlite", "file:"+path+suffix)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close sqlite: %v", cerr)
		}
	})
	return db
}
