// Package sql_test — the migration suite: the order, the lock, the dry run,
// and every refusal that stands between two instances starting together and
// one migration applied twice.
package sql_test

import (
	"context"
	stdsql "database/sql"
	"database/sql/driver"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsql "github.com/kitsunium/sdk/internal/service/sql"
)

// The exact statements the runner sends on PostgreSQL. Spelling them out here
// rather than rebuilding them from the same helpers the production code uses
// is deliberate: a test that computes its expectation the way the code does
// preserves exactly the bug it should catch.
const (
	tryLock     = "SELECT pg_try_advisory_lock($1)"
	unlock      = "SELECT pg_advisory_unlock($1)"
	readApplied = "SELECT version FROM schema_migrations ORDER BY version"
	createTable = "CREATE TABLE IF NOT EXISTS schema_migrations " +
		"(version BIGINT NOT NULL PRIMARY KEY, name TEXT NOT NULL, applied_at_unix BIGINT NOT NULL)"
	recordRow = "INSERT INTO schema_migrations (version, name, applied_at_unix) VALUES ($1, $2, $3)"
	forgetRow = "DELETE FROM schema_migrations WHERE version = $1"
)

// lockBudget / lockRetry drive the acquisition loop deterministically: one
// retry interval is exactly one budget, so a second refusal ends the wait.
const (
	lockBudget time.Duration = time.Second
	lockRetry  time.Duration = time.Second
)

// step returns a migration Step that records its own name in order.
func step(log *[]string, label string) coresql.Step {
	return func(ctx context.Context, ex coresql.Executor) error {
		*log = append(*log, label)
		_, err := ex.ExecContext(ctx, label)
		return err
	}
}

// grantLock scripts the advisory lock as immediately available.
func grantLock(f *fakeDB) *fakeDB {
	f.rowsOn(tryLock, "locked", true)
	f.rowsOn(unlock, "unlocked", true)
	return f
}

// newMigrator wires a runner over a scripted server and a manual clock, and
// hands back the pool so a test can assert no connection stayed checked out.
func newMigrator(
	t *testing.T, f *fakeDB, clk clock.Timed, migrations ...coresql.MigrationValue,
) (coresql.Migrator, *stdsql.DB) {
	t.Helper()
	runner, db, err := buildMigrator(t, f, clk, svcsql.MigrateConfig{
		Migrations: migrations, LockTimeout: lockBudget, LockRetryInterval: lockRetry,
	})
	if err != nil {
		t.Fatalf("NewMigrator: %v", err)
	}
	return runner, db
}

// buildMigrator is the constructor half, kept separate so refusal tests can
// assert on the error instead of failing the test.
func buildMigrator(
	t *testing.T, f *fakeDB, clk clock.Timed, mig svcsql.MigrateConfig,
) (coresql.Migrator, *stdsql.DB, error) {
	t.Helper()
	db := closeOnCleanup(t, f.open())
	runner, err := svcsql.NewMigrator(svcsql.Config{
		DB: db, Dialect: coresql.DialectPostgres, Clock: clk,
		//: at least two: the lock holds one connection for the whole run, and
		//: every migration transaction needs another.
		Pool: svcsql.PoolConfig{MaxOpen: 4},
	}, mig)
	return runner, db, err
}

// TestMigratorRefusesADialectWithNoAdvisoryLock is the refusal that matters
// most, because the alternative is a runner that drops mutual exclusion
// silently in exactly the case it exists for.
func TestMigratorRefusesADialectWithNoAdvisoryLock(t *testing.T) {
	t.Parallel()
	db := closeOnCleanup(t, newFakeDB().open())
	_, err := svcsql.NewMigrator(svcsql.Config{
		DB: db, Dialect: coresql.DialectSQLite, Pool: svcsql.PoolConfig{MaxOpen: 2},
	}, svcsql.MigrateConfig{})
	if !errs.HasCode(err, svcsql.CodeMigrationLockUnsupported) {
		t.Fatalf("NewMigrator(sqlite) = %v, want MIGRATION_LOCK_UNSUPPORTED", err)
	}
}

// TestPlanIsADryRunThatAppliesNothingAndTakesNoLock pins both halves of the
// dry run, including the one thing it DOES write — its own bookkeeping table,
// because reading a table that does not exist is not portable.
func TestPlanIsADryRunThatAppliesNothingAndTakesNoLock(t *testing.T) {
	t.Parallel()
	f := grantLock(newFakeDB())
	var applied []string
	runner, _ := newMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
		coresql.MigrationValue{Version: 1, Name: "one", Up: step(&applied, "UP 1"), Down: coresql.Irreversible},
		coresql.MigrationValue{Version: 2, Name: "two", Up: step(&applied, "UP 2"), Down: coresql.Irreversible},
	)
	pending, err := runner.Plan(t.Context())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(pending) != 2 || pending[0].Version != 1 || pending[1].Version != 2 {
		t.Fatalf("Plan = %v, want versions 1 then 2", pending)
	}
	if len(applied) != 0 {
		t.Fatalf("Plan ran %v — a dry run applies nothing", applied)
	}
	if f.sent(tryLock) {
		t.Fatal("Plan took the migration lock; a dry run must not block behind a real run")
	}
	if !f.sent(createTable) {
		t.Fatal("Plan did not ensure the version table, so its read would depend on a driver error string")
	}
	if f.sent(recordRow) {
		t.Fatal("Plan wrote a version row")
	}
}

// TestUpAppliesInAscendingOrderAndRecordsEachVersion is the ordinary path,
// asserted as a SEQUENCE because the order is the contract.
func TestUpAppliesInAscendingOrderAndRecordsEachVersion(t *testing.T) {
	t.Parallel()
	f := grantLock(newFakeDB())
	var applied []string
	//: declared out of order on purpose — the runner sorts by version.
	runner, _ := newMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
		coresql.MigrationValue{Version: 3, Name: "three", Up: step(&applied, "UP 3"), Down: coresql.Irreversible},
		coresql.MigrationValue{Version: 1, Name: "one", Up: step(&applied, "UP 1"), Down: coresql.Irreversible},
		coresql.MigrationValue{Version: 2, Name: "two", Up: step(&applied, "UP 2"), Down: coresql.Irreversible},
	)
	if err := runner.Up(t.Context()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if want := []string{"UP 1", "UP 2", "UP 3"}; !slices.Equal(applied, want) {
		t.Fatalf("applied %v, want %v", applied, want)
	}
	records := 0
	for _, stmt := range f.statements() {
		if stmt == recordRow {
			records++
		}
	}
	if records != 3 {
		t.Fatalf("wrote %d version rows for 3 migrations", records)
	}
}

// TestUpSkipsWhatIsAlreadyApplied pins the set difference: the version IS the
// identity, so a recorded migration is never re-run.
func TestUpSkipsWhatIsAlreadyApplied(t *testing.T) {
	t.Parallel()
	f := grantLock(newFakeDB())
	f.rowsOn(readApplied, "version", int64(1))
	var applied []string
	runner, _ := newMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
		coresql.MigrationValue{Version: 1, Name: "one", Up: step(&applied, "UP 1"), Down: coresql.Irreversible},
		coresql.MigrationValue{Version: 2, Name: "two", Up: step(&applied, "UP 2"), Down: coresql.Irreversible},
	)
	if err := runner.Up(t.Context()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if want := []string{"UP 2"}; !slices.Equal(applied, want) {
		t.Fatalf("applied %v, want %v", applied, want)
	}
}

// TestUpRefusesAMigrationOlderThanOneApplied is the two-branches-merged
// hazard: both numbered from the same base, one shipped first, and the other
// would now slot underneath it.
func TestUpRefusesAMigrationOlderThanOneApplied(t *testing.T) {
	t.Parallel()
	f := grantLock(newFakeDB())
	f.rowsOn(readApplied, "version", int64(20))
	var applied []string
	runner, _ := newMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
		coresql.MigrationValue{Version: 10, Name: "late arrival", Up: step(&applied, "UP 10"), Down: coresql.Irreversible},
		coresql.MigrationValue{Version: 20, Name: "shipped", Up: step(&applied, "UP 20"), Down: coresql.Irreversible},
	)
	err := runner.Up(t.Context())
	if !errs.HasCode(err, svcsql.CodeMigrationOutOfOrder) {
		t.Fatalf("Up = %v, want MIGRATION_OUT_OF_ORDER", err)
	}
	if len(applied) != 0 {
		t.Fatalf("applied %v before refusing", applied)
	}
}

// TestUpTakesTheLockFirstAndGivesItBack pins the ordering that makes the
// mutual-exclusion promise true, and that the dedicated connection is
// returned rather than leaked for the life of the process.
func TestUpTakesTheLockFirstAndGivesItBack(t *testing.T) {
	t.Parallel()
	f := grantLock(newFakeDB())
	var applied []string
	runner, db := newMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
		coresql.MigrationValue{Version: 1, Name: "one", Up: step(&applied, "UP 1"), Down: coresql.Irreversible},
	)
	if err := runner.Up(t.Context()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	stmts := f.statements()
	lockAt := slices.Index(stmts, tryLock)
	workAt := slices.Index(stmts, "UP 1")
	unlockAt := slices.Index(stmts, unlock)
	if lockAt < 0 || workAt < 0 || unlockAt < 0 {
		t.Fatalf("statements = %v, want lock, work and unlock", stmts)
	}
	if lockAt >= workAt || workAt >= unlockAt {
		t.Fatalf("statements = %v, want the lock taken before the work and released after", stmts)
	}
	//: Close on a *sql.Conn returns it to the POOL, so the honest assertion
	//: is that nothing stayed checked out — not that the driver connection
	//: was destroyed, which the pool is right to avoid.
	if inUse := db.Stats().InUse; inUse != 0 {
		t.Fatalf("%d connections still checked out after Up — the lock connection leaked", inUse)
	}
}

// TestUpAppliesNothingWhenTheLockIsHeldForTheWholeBudget is the two-instances
// case. The clock is manual, so the budget is asserted exactly and the test
// sleeps for nothing.
func TestUpAppliesNothingWhenTheLockIsHeldForTheWholeBudget(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	//: another process holds it, always.
	f.rowsOn(tryLock, "locked", false)
	var applied []string
	manual := clock.NewManualClock(time.Unix(0, 0))
	runner, db := newMigrator(t, f, manual,
		coresql.MigrationValue{Version: 1, Name: "one", Up: step(&applied, "UP 1"), Down: coresql.Irreversible},
	)
	verdict := make(chan error, 1)
	//: Up runs on its own goroutine because it is BLOCKED in the lock retry
	//: loop while the manual clock advances. The WaitGroup makes the join
	//: explicit, so the test cannot finish with the runner still holding a
	//: connection.
	var running sync.WaitGroup
	running.Go(func() { verdict <- runner.Up(t.Context()) })
	t.Cleanup(running.Wait)
	//: the retry wait must be REGISTERED before the clock moves, or the
	//: advance would happen into an empty wait set and the test would hang.
	manual.BlockUntil(1)
	manual.Advance(lockRetry)
	err := <-verdict
	if !errs.HasCode(err, svcsql.CodeMigrationLockTimeout) {
		t.Fatalf("Up = %v, want MIGRATION_LOCK_TIMEOUT", err)
	}
	if len(applied) != 0 {
		t.Fatalf("applied %v while the lock was held by somebody else", applied)
	}
	if f.sent(createTable) {
		t.Fatal("the runner touched the schema before holding the lock")
	}
	//: a failed acquisition must not keep the connection it was going to
	//: hold — the pool would be one short for the life of the process.
	if inUse := db.Stats().InUse; inUse != 0 {
		t.Fatalf("%d connections still checked out after a failed acquisition", inUse)
	}
}

// TestAMigrationAndItsVersionRowCommitTogether pins the atomicity claim on an
// engine with transactional DDL: a failing step writes no version row and the
// transaction is rolled back.
func TestAMigrationAndItsVersionRowCommitTogether(t *testing.T) {
	t.Parallel()
	f := grantLock(newFakeDB())
	f.failOn("UP 1")
	var applied []string
	runner, _ := newMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
		coresql.MigrationValue{Version: 1, Name: "one", Up: step(&applied, "UP 1"), Down: coresql.Irreversible},
	)
	err := runner.Up(t.Context())
	if !errs.HasCode(err, svcsql.CodeMigrationFailed) {
		t.Fatalf("Up = %v, want MIGRATION_FAILED", err)
	}
	if f.sent(recordRow) {
		t.Fatal("a failed migration recorded its version")
	}
	if !f.sent("ROLLBACK") {
		t.Fatalf("statements = %v, want a ROLLBACK", f.statements())
	}
	if fieldValue(errs.FieldsOf(err), "direction") != "up" {
		t.Fatal("the failure did not say which way the schema was moving")
	}
}

// TestDownReversesInDescendingOrderAndForgetsEachVersion is the mirror path.
func TestDownReversesInDescendingOrderAndForgetsEachVersion(t *testing.T) {
	t.Parallel()
	f := grantLock(newFakeDB())
	f.rowsOn(readApplied, "version", int64(1), int64(2), int64(3))
	var reversed []string
	noop := func(context.Context, coresql.Executor) error { return nil }
	runner, _ := newMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
		coresql.MigrationValue{Version: 1, Name: "one", Up: noop, Down: step(&reversed, "DOWN 1")},
		coresql.MigrationValue{Version: 2, Name: "two", Up: noop, Down: step(&reversed, "DOWN 2")},
		coresql.MigrationValue{Version: 3, Name: "three", Up: noop, Down: step(&reversed, "DOWN 3")},
	)
	if err := runner.Down(t.Context(), 1); err != nil {
		t.Fatalf("Down: %v", err)
	}
	//: version 1 is AT the target, so it stays.
	if want := []string{"DOWN 3", "DOWN 2"}; !slices.Equal(reversed, want) {
		t.Fatalf("reversed %v, want %v", reversed, want)
	}
	forgets := 0
	for _, stmt := range f.statements() {
		if stmt == forgetRow {
			forgets++
		}
	}
	if forgets != 2 {
		t.Fatalf("removed %d version rows for 2 reversals", forgets)
	}
}

// TestDownRefusesAnIrreversibleMigration pins that the caller's own
// declaration comes back as a typed, matchable answer.
func TestDownRefusesAnIrreversibleMigration(t *testing.T) {
	t.Parallel()
	f := grantLock(newFakeDB())
	f.rowsOn(readApplied, "version", int64(1))
	noop := func(context.Context, coresql.Executor) error { return nil }
	runner, _ := newMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
		coresql.MigrationValue{Version: 1, Name: "one", Up: noop, Down: coresql.Irreversible},
	)
	err := runner.Down(t.Context(), 0)
	if !errs.HasCode(err, coresql.CodeMigrationIrreversible) {
		t.Fatalf("Down = %v, want MIGRATION_IRREVERSIBLE", err)
	}
	if f.sent(forgetRow) {
		t.Fatal("an irreversible migration was forgotten from the version table")
	}
}

// TestDownRefusesBeforeReversingAnythingWhenTheDatabaseIsAhead is the
// deployment state where a rollback to a previous image leaves the database
// recording a migration the running binary does not carry. Reversing the known
// ones first would undo them underneath a change still in place.
func TestDownRefusesBeforeReversingAnythingWhenTheDatabaseIsAhead(t *testing.T) {
	t.Parallel()
	f := grantLock(newFakeDB())
	f.rowsOn(readApplied, "version", int64(1), int64(999))
	var reversed []string
	noop := func(context.Context, coresql.Executor) error { return nil }
	runner, _ := newMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
		coresql.MigrationValue{Version: 1, Name: "one", Up: noop, Down: step(&reversed, "DOWN 1")},
	)
	err := runner.Down(t.Context(), 0)
	if !errs.HasCode(err, svcsql.CodeMigrationUnknownVersion) {
		t.Fatalf("Down = %v, want MIGRATION_UNKNOWN_VERSION", err)
	}
	if len(reversed) != 0 {
		t.Fatalf("reversed %v before discovering the unknown version", reversed)
	}
}

// TestDuplicateVersionsAreRefusedAtConstruction pins that the identity stays
// unique, because a duplicate makes "has this been applied?" unanswerable.
func TestDuplicateVersionsAreRefusedAtConstruction(t *testing.T) {
	t.Parallel()
	noop := func(context.Context, coresql.Executor) error { return nil }
	_, _, err := buildMigrator(t, newFakeDB(), clock.NewManualClock(time.Unix(0, 0)), svcsql.MigrateConfig{
		Migrations: []coresql.MigrationValue{
			{Version: 1, Name: "one", Up: noop, Down: noop},
			{Version: 1, Name: "one again", Up: noop, Down: noop},
		},
	})
	if !errs.HasCode(err, svcsql.CodeDuplicateMigration) {
		t.Fatalf("NewMigrator = %v, want DUPLICATE_MIGRATION", err)
	}
}

// TestTheMigrationSetIsCopiedAtConstruction pins that a caller appending to
// their slice afterwards cannot change what the runner will apply.
func TestTheMigrationSetIsCopiedAtConstruction(t *testing.T) {
	t.Parallel()
	f := grantLock(newFakeDB())
	var applied []string
	set := []coresql.MigrationValue{
		{Version: 1, Name: "one", Up: step(&applied, "UP 1"), Down: coresql.Irreversible},
	}
	runner, _, err := buildMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)), svcsql.MigrateConfig{
		Migrations: set, LockTimeout: lockBudget, LockRetryInterval: lockRetry,
	})
	if err != nil {
		t.Fatalf("NewMigrator: %v", err)
	}
	//: the caller appends AFTER construction. The runner took its own copy,
	//: so the smuggled migration must never run — asserting the append
	//: happened keeps the test honest about what it is proving.
	set = append(set, coresql.MigrationValue{
		Version: 2, Name: "smuggled", Up: step(&applied, "UP 2"), Down: coresql.Irreversible,
	})
	if len(set) != 2 {
		t.Fatalf("len(set) = %d, want 2 — the append this test depends on did not happen", len(set))
	}
	if err := runner.Up(t.Context()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if want := []string{"UP 1"}; !slices.Equal(applied, want) {
		t.Fatalf("applied %v, want %v — the set was not copied", applied, want)
	}
}

// TestVersionTableMustBeAPlainIdentifier pins the only thing standing between
// a configuration string and an injection: a table name cannot be bound as a
// parameter, so it is interpolated.
func TestVersionTableMustBeAPlainIdentifier(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"schema migrations", "public.schema_migrations", `"quoted"`,
		"t; DROP TABLE accounts", "1_leading_digit", "t--comment",
	} {
		_, _, err := buildMigrator(t, newFakeDB(), clock.NewManualClock(time.Unix(0, 0)),
			svcsql.MigrateConfig{VersionTable: name})
		if !errs.HasCode(err, svcsql.CodeVersionTableInvalid) {
			t.Fatalf("VersionTable=%q: NewMigrator = %v, want VERSION_TABLE_INVALID", name, err)
		}
	}
}

// TestAnUnreadableVersionTableStopsTheRun pins that a failed read never
// produces a plan — every answer would be a guess.
func TestAnUnreadableVersionTableStopsTheRun(t *testing.T) {
	t.Parallel()
	f := grantLock(newFakeDB())
	f.failOn(readApplied)
	var applied []string
	runner, _ := newMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
		coresql.MigrationValue{Version: 1, Name: "one", Up: step(&applied, "UP 1"), Down: coresql.Irreversible},
	)
	if err := runner.Up(t.Context()); !errs.HasCode(err, svcsql.CodeMigrationFailed) {
		t.Fatalf("Up = %v, want MIGRATION_FAILED", err)
	}
	if len(applied) != 0 {
		t.Fatalf("applied %v against an unreadable history", applied)
	}
}

// TestStatementsRunsInOrderAndStopsAtTheFirstFailure pins the one helper the
// SDK ships for turning SQL text into a step.
func TestStatementsRunsInOrderAndStopsAtTheFirstFailure(t *testing.T) {
	t.Parallel()
	f := grantLock(newFakeDB())
	f.failOn("SECOND")
	runner, _ := newMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
		coresql.MigrationValue{
			Version: 1, Name: "one",
			Up:   svcsql.Statements("FIRST", "SECOND", "THIRD"),
			Down: coresql.Irreversible,
		},
	)
	if err := runner.Up(t.Context()); !errs.HasCode(err, svcsql.CodeMigrationFailed) {
		t.Fatalf("Up = %v, want MIGRATION_FAILED", err)
	}
	if !f.sent("FIRST") || !f.sent("SECOND") {
		t.Fatalf("statements = %v, want FIRST then SECOND", f.statements())
	}
	if f.sent("THIRD") {
		t.Fatal("the step continued past a failed statement")
	}
}

// TestTheLockAnswerIsReadInBothEnginesShapes pins the two contracts this
// package actually supports: PostgreSQL answers with a boolean, MySQL with
// 1/0, and a driver may hand either back as bytes.
func TestTheLockAnswerIsReadInBothEnginesShapes(t *testing.T) {
	t.Parallel()
	cases := map[string]driver.Value{
		"postgres-bool": true, "mysql-int": int64(1), "text-bytes": []byte("1"),
	}
	for label, answer := range cases {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			f := newFakeDB()
			f.rowsOn(tryLock, "locked", answer)
			f.rowsOn(unlock, "unlocked", answer)
			var applied []string
			runner, _ := newMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
				coresql.MigrationValue{Version: 1, Name: "one", Up: step(&applied, "UP 1"), Down: coresql.Irreversible},
			)
			if err := runner.Up(t.Context()); err != nil {
				t.Fatalf("Up = %v, want the lock to be read as granted", err)
			}
		})
	}
}

// TestARejectedLockStatementIsNotABusySignal pins the distinction: a
// statement the engine REFUSED is a failure, not "somebody else holds it".
func TestARejectedLockStatementIsNotABusySignal(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	f.failOn(tryLock)
	runner, _ := newMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)))
	err := runner.Up(t.Context())
	if !errs.HasCode(err, svcsql.CodeMigrationFailed) {
		t.Fatalf("Up = %v, want MIGRATION_FAILED", err)
	}
	if errors.Is(err, nil) || fieldValue(errs.FieldsOf(err), "phase") != "lock" {
		t.Fatal("the failure did not name the phase it died in")
	}
}

// fieldValue returns the string value of an error field, or "".
func fieldValue(fields []errs.FieldValue, key string) string {
	for _, field := range fields {
		if field.Key() == key {
			return field.StringValue()
		}
	}
	return ""
}
