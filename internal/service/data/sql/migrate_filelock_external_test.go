// Package sql_test — the SQLite migration runner: its lock is the database
// file's write lock, held by ONE transaction for the whole run, with every
// migration in a savepoint of it (ADR 0140).
package sql_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"
)

// The statements the SQLite runner sends, spelled out for the reason the
// PostgreSQL ones are: a test that rebuilt them with the production helpers
// would keep exactly the bug it should catch.
const (
	fileLock        = "DELETE FROM schema_migrations WHERE 1 = 0"
	sqliteRecordRow = "INSERT INTO schema_migrations (version, name, applied_at_unix) VALUES (?, ?, ?)"
	sqliteForgetRow = "DELETE FROM schema_migrations WHERE version = ?"
)

// errBusy is SQLite's own answer when another connection holds the write
// lock: sqlite3_errstr's text for SQLITE_BUSY.
var errBusy = errors.New("database is locked")

// newSQLiteMigrator wires a SQLite runner over a scripted server, with a pool
// of ONE connection: the run needs no second one, unlike an advisory lock.
func newSQLiteMigrator(
	t *testing.T, f *fakeDB, clk clock.Timed, migrations ...coresql.MigrationValue,
) coresql.Migrator {
	t.Helper()
	db := closeOnCleanup(t, f.open())
	runner, err := svcsql.NewMigrator(svcsql.Config{
		DB: db, Dialect: coresql.DialectSQLite, Clock: clk,
		Pool: svcsql.PoolConfig{MaxOpen: 1},
	}, svcsql.MigrateConfig{Migrations: migrations, LockTimeout: lockBudget, LockRetryInterval: lockRetry})
	if err != nil {
		t.Fatalf("NewMigrator(sqlite): %v", err)
	}
	t.Cleanup(func() {
		if inUse := db.Stats().InUse; inUse != 0 {
			t.Errorf("%d connections still checked out after the run", inUse)
		}
	})
	return runner
}

// busyThenGranted scripts the lock statement to answer busy the first time
// and to take the lock after.
func busyThenGranted(call int) ([]string, [][]driver.Value, error) {
	if call == 0 {
		return nil, nil, errBusy
	}
	return nil, nil, nil
}

// TestSQLiteMigratesUnderTheFileWriteLock is the ordinary path, asserted as a
// sequence: one transaction, the lock taken by its first statements, the plan
// read inside it, each migration in a savepoint with its version row, one
// COMMIT — and none of the advisory-lock statements, which SQLite does not
// have.
func TestSQLiteMigratesUnderTheFileWriteLock(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	var applied []string
	runner := newSQLiteMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
		coresql.MigrationValue{Version: 1, Name: "one", Up: step(&applied, "UP 1"), Down: coresql.Irreversible},
		coresql.MigrationValue{Version: 2, Name: "two", Up: step(&applied, "UP 2"), Down: coresql.Irreversible},
	)
	if err := runner.Up(t.Context()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	want := []string{
		"BEGIN", createTable, fileLock,
		createTable, readApplied,
		"SAVEPOINT ktn_sp_1", "UP 1", sqliteRecordRow, "RELEASE SAVEPOINT ktn_sp_1",
		"SAVEPOINT ktn_sp_2", "UP 2", sqliteRecordRow, "RELEASE SAVEPOINT ktn_sp_2",
		"COMMIT",
	}
	if got := f.statements(); !slices.Equal(got, want) {
		t.Fatalf("statements =\n%v\nwant\n%v", got, want)
	}
}

// TestSQLiteWaitsWhileAnotherWriterHoldsTheFile is the two-instances case: a
// busy answer rolls the attempt back, waits one interval on the injected
// clock, and tries again.
func TestSQLiteWaitsWhileAnotherWriterHoldsTheFile(t *testing.T) {
	t.Parallel()
	f := newFakeDB().on(fileLock, busyThenGranted)
	var applied []string
	manual := clock.NewManualClock(time.Unix(0, 0))
	runner := newSQLiteMigrator(t, f, manual,
		coresql.MigrationValue{Version: 1, Name: "one", Up: step(&applied, "UP 1"), Down: coresql.Irreversible},
	)
	verdict := make(chan error, 1)
	var running sync.WaitGroup
	running.Go(func() { verdict <- runner.Up(t.Context()) })
	t.Cleanup(running.Wait)
	manual.BlockUntil(1)
	if len(applied) != 0 {
		t.Fatalf("applied %v while another connection held the file", applied)
	}
	manual.Advance(lockRetry)
	if err := <-verdict; err != nil {
		t.Fatalf("Up after the lock came free: %v", err)
	}
	stmts := f.statements()
	if first := slices.Index(stmts, "ROLLBACK"); first < 0 || first > slices.Index(stmts, "UP 1") {
		t.Fatalf("statements = %v, want the busy attempt rolled back before the work", stmts)
	}
	if !slices.Equal(applied, []string{"UP 1"}) {
		t.Fatalf("applied %v, want UP 1 once", applied)
	}
}

// TestSQLiteReadsABusyBeginAsTheLockHeldElsewhere pins the other place SQLite
// answers a held lock: at BEGIN itself, for a connection opened with
// _txlock=immediate — which takes the lock there — or one that converts a
// fresh file to WAL as it opens. The attempt is retried like a busy lock
// statement, not reported as a failure.
func TestSQLiteReadsABusyBeginAsTheLockHeldElsewhere(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	f.beginFailures = []error{errBusy}
	var applied []string
	manual := clock.NewManualClock(time.Unix(0, 0))
	runner := newSQLiteMigrator(t, f, manual,
		coresql.MigrationValue{Version: 1, Name: "one", Up: step(&applied, "UP 1"), Down: coresql.Irreversible},
	)
	verdict := make(chan error, 1)
	var running sync.WaitGroup
	running.Go(func() { verdict <- runner.Up(t.Context()) })
	t.Cleanup(running.Wait)
	manual.BlockUntil(1)
	manual.Advance(lockRetry)
	if err := <-verdict; err != nil {
		t.Fatalf("Up after a busy BEGIN: %v", err)
	}
	if !slices.Equal(applied, []string{"UP 1"}) {
		t.Fatalf("applied %v, want UP 1 once", applied)
	}
}

// TestSQLiteGivesUpWhenTheFileStaysLocked pins the budget: a lock held for the
// whole of it applies nothing and says so with the code a caller retries on.
func TestSQLiteGivesUpWhenTheFileStaysLocked(t *testing.T) {
	t.Parallel()
	f := newFakeDB().on(fileLock, func(int) ([]string, [][]driver.Value, error) { return nil, nil, errBusy })
	var applied []string
	manual := clock.NewManualClock(time.Unix(0, 0))
	runner := newSQLiteMigrator(t, f, manual,
		coresql.MigrationValue{Version: 1, Name: "one", Up: step(&applied, "UP 1"), Down: coresql.Irreversible},
	)
	verdict := make(chan error, 1)
	var running sync.WaitGroup
	running.Go(func() { verdict <- runner.Up(t.Context()) })
	t.Cleanup(running.Wait)
	manual.BlockUntil(1)
	manual.Advance(lockRetry)
	err := <-verdict
	if !errs.HasCode(err, svcsql.CodeMigrationLockTimeout) {
		t.Fatalf("Up = %v, want MIGRATION_LOCK_TIMEOUT", err)
	}
	if len(applied) != 0 || f.sent(sqliteRecordRow) || f.sent("COMMIT") {
		t.Fatalf("statements = %v, want nothing applied and nothing committed", f.statements())
	}
}

// TestABusyAttemptWhoseRollbackFailsIsNotRetried pins the one busy answer
// that is not waited out: undoing the attempt failed, so what the connection
// still holds is unknown. The run stops with the rollback's verdict instead of
// registering a wait — which the loop below would catch.
func TestABusyAttemptWhoseRollbackFailsIsNotRetried(t *testing.T) {
	t.Parallel()
	f := newFakeDB().on(fileLock, func(int) ([]string, [][]driver.Value, error) { return nil, nil, errBusy })
	f.rollbackErr = errScripted
	manual := clock.NewManualClock(time.Unix(0, 0))
	runner := newSQLiteMigrator(t, f, manual)
	verdict := make(chan error, 1)
	var running sync.WaitGroup
	running.Go(func() { verdict <- runner.Up(t.Context()) })
	t.Cleanup(running.Wait)
	for {
		select {
		case err := <-verdict:
			if !errs.HasCode(err, svcsql.CodeRollbackFailed) {
				t.Fatalf("Up = %v, want ROLLBACK_FAILED beside the busy answer", err)
			}
			return
		default:
			if manual.Pending() > 0 {
				manual.Advance(lockRetry)
				t.Fatal("the run waited to retry after its rollback failed")
			}
			runtime.Gosched()
		}
	}
}

// TestABeginRefusedForAnotherReasonNamesTheLockPhase pins where a BEGIN that
// is refused, and not busy, is reported: in the lock phase, since no migration
// started — never as a commit that lost the run.
func TestABeginRefusedForAnotherReasonNamesTheLockPhase(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	f.beginErr = errScripted
	runner := newSQLiteMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)))
	err := runner.Up(t.Context())
	if !errs.HasCode(err, svcsql.CodeMigrationFailed) || fieldValue(errs.FieldsOf(err), "phase") != "lock" {
		t.Fatalf("Up = %v, want MIGRATION_FAILED in the lock phase", err)
	}
	if !errs.HasCode(err, svcsql.CodeBeginFailed) {
		t.Fatal("the transactor's own verdict did not travel beside the run's")
	}
}

// TestASQLiteRefusalThatIsNotBusyStopsAtOnce pins the other reading: a lock
// statement SQLite refused for any other reason is a failure, reported at
// once and never waited on.
func TestASQLiteRefusalThatIsNotBusyStopsAtOnce(t *testing.T) {
	t.Parallel()
	f := newFakeDB().failOn(fileLock)
	runner := newSQLiteMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)))
	err := runner.Up(t.Context())
	if !errs.HasCode(err, svcsql.CodeMigrationFailed) || fieldValue(errs.FieldsOf(err), "phase") != "lock" {
		t.Fatalf("Up = %v, want MIGRATION_FAILED in the lock phase", err)
	}
	if !errors.Is(err, errScripted) {
		t.Fatal("the driver's own error did not survive")
	}
}

// TestASQLiteMigrationThatFailsKeepsTheOnesBeforeIt pins that each migration
// is still atomic on its own: the failing one rolls back to its savepoint, the
// run COMMITS the ones before it, and stops.
func TestASQLiteMigrationThatFailsKeepsTheOnesBeforeIt(t *testing.T) {
	t.Parallel()
	f := newFakeDB().failOn("UP 2")
	var applied []string
	runner := newSQLiteMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
		coresql.MigrationValue{Version: 1, Name: "one", Up: step(&applied, "UP 1"), Down: coresql.Irreversible},
		coresql.MigrationValue{Version: 2, Name: "two", Up: step(&applied, "UP 2"), Down: coresql.Irreversible},
		coresql.MigrationValue{Version: 3, Name: "three", Up: step(&applied, "UP 3"), Down: coresql.Irreversible},
	)
	err := runner.Up(t.Context())
	if !errs.HasCode(err, svcsql.CodeMigrationFailed) || fieldValue(errs.FieldsOf(err), "version") != "2" {
		t.Fatalf("Up = %v, want MIGRATION_FAILED naming version 2", err)
	}
	want := []string{
		"SAVEPOINT ktn_sp_1", "UP 1", sqliteRecordRow, "RELEASE SAVEPOINT ktn_sp_1",
		"SAVEPOINT ktn_sp_2", "UP 2", "ROLLBACK TO SAVEPOINT ktn_sp_2",
		"COMMIT",
	}
	stmts := f.statements()
	if got := stmts[slices.Index(stmts, "SAVEPOINT ktn_sp_1"):]; !slices.Equal(got, want) {
		t.Fatalf("statements after the plan =\n%v\nwant\n%v", got, want)
	}
	if slices.Contains(applied, "UP 3") {
		t.Fatal("the run went on past the failed migration")
	}
}

// TestASQLiteRunWhoseCommitFailsSaysItLostTheRun pins the one way the SQLite
// run differs from a run of one transaction per migration: its COMMIT carries
// every migration it applied, so a refused COMMIT is named as the phase that
// lost them all.
func TestASQLiteRunWhoseCommitFailsSaysItLostTheRun(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	f.commitErr = errScripted
	var applied []string
	runner := newSQLiteMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
		coresql.MigrationValue{Version: 1, Name: "one", Up: step(&applied, "UP 1"), Down: coresql.Irreversible},
	)
	err := runner.Up(t.Context())
	if !errs.HasCode(err, svcsql.CodeMigrationFailed) || fieldValue(errs.FieldsOf(err), "phase") != "commit" {
		t.Fatalf("Up = %v, want MIGRATION_FAILED in the commit phase", err)
	}
	if !errs.HasCode(err, svcsql.CodeCommitFailed) {
		t.Fatal("the transaction's own verdict did not travel beside the run's")
	}
}

// TestSQLiteDownReversesUnderTheSameLock is the mirror path: descending, each
// reversal in its own savepoint with its version row forgotten, one COMMIT.
func TestSQLiteDownReversesUnderTheSameLock(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	f.rowsOn(readApplied, "version", int64(1), int64(2))
	var reversed []string
	noop := func(context.Context, coresql.Executor) error { return nil }
	runner := newSQLiteMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
		coresql.MigrationValue{Version: 1, Name: "one", Up: noop, Down: step(&reversed, "DOWN 1")},
		coresql.MigrationValue{Version: 2, Name: "two", Up: noop, Down: step(&reversed, "DOWN 2")},
	)
	if err := runner.Down(t.Context(), 0); err != nil {
		t.Fatalf("Down: %v", err)
	}
	want := []string{
		"BEGIN", createTable, fileLock, createTable, readApplied,
		"SAVEPOINT ktn_sp_1", "DOWN 2", sqliteForgetRow, "RELEASE SAVEPOINT ktn_sp_1",
		"SAVEPOINT ktn_sp_2", "DOWN 1", sqliteForgetRow, "RELEASE SAVEPOINT ktn_sp_2",
		"COMMIT",
	}
	if got := f.statements(); !slices.Equal(got, want) {
		t.Fatalf("statements =\n%v\nwant\n%v", got, want)
	}
}

// TestSQLitePlanTakesNoLock pins the dry run on SQLite: no transaction and no
// lock statement, only the bookkeeping table and its read.
func TestSQLitePlanTakesNoLock(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	runner := newSQLiteMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
		coresql.MigrationValue{Version: 1, Name: "one", Up: coresql.Irreversible, Down: coresql.Irreversible},
	)
	pending, err := runner.Plan(t.Context())
	if err != nil || len(pending) != 1 {
		t.Fatalf("Plan = %v, %v, want the one migration pending", pending, err)
	}
	if got, want := f.statements(), []string{createTable, readApplied}; !slices.Equal(got, want) {
		t.Fatalf("statements = %v, want %v", got, want)
	}
}
