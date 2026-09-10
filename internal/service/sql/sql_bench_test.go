// Package sql_test — the benchmark suite.
//
// # What is measured, and what is deliberately not
//
// Every benchmark here runs against the scripted driver from
// harness_external_test.go, so no number below includes a network, a server,
// a parser or a disk. That is the point: what this package COSTS is the
// transaction bookkeeping it adds on top of database/sql, and the only way to
// see it is to hold everything else at zero.
//
// The consequence is stated rather than implied: these are NOT numbers for
// "how fast is a transaction". A real BEGIN/COMMIT pair against PostgreSQL is
// two network round trips, which is three to four orders of magnitude more
// than anything measured here. The useful reading is the DELTA against the
// stdlib baselines below — Std* — which run the identical driver calls
// through database/sql alone. That delta is the SDK's whole price, and it is
// the one figure a reader can act on.
package sql_test

import (
	"context"
	stdsql "database/sql"
	"database/sql/driver"
	"errors"
	"strconv"
	"testing"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svcsql "github.com/kitsunium/sdk/internal/service/sql"
)

// benchStmt is the statement every transaction benchmark runs inside its
// scope, so the SDK's overhead is measured around a constant.
const benchStmt string = "UPDATE t SET c = 1"

// benchSetSize is how many migrations the migration benchmarks carry, and how
// many rows PlanFullHistory scans. Named so BENCH.md's prose and the code
// cannot drift apart.
const benchSetSize int = 64

// benchDB opens a silent scripted pool. The caller closes it.
func benchDB(b *testing.B, f *fakeDB) *stdsql.DB {
	b.Helper()
	db := closeOnCleanup(b, f.silent().open())
	return db
}

// benchTransactor wires a manager over a silent scripted pool.
func benchTransactor(b *testing.B) coresql.Transactor {
	b.Helper()
	tm, err := svcsql.NewTransactor(svcsql.Config{
		DB: benchDB(b, newFakeDB()), Dialect: coresql.DialectPostgres,
		Pool: svcsql.PoolConfig{MaxOpen: 8},
	})
	if err != nil {
		b.Fatalf("NewTransactor: %v", err)
	}
	return tm
}

// BenchmarkTransactCommit is the ordinary path: open, one statement, commit.
func BenchmarkTransactCommit(b *testing.B) {
	tm := benchTransactor(b)
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		err := tm.Transact(ctx, coresql.TxOptionsValue{}, func(c context.Context, ex coresql.Executor) error {
			_, execErr := ex.ExecContext(c, benchStmt)
			return execErr
		})
		if err != nil {
			b.Fatalf("Transact: %v", err)
		}
	}
}

// BenchmarkStdTransactCommit is the baseline: the SAME three driver calls
// straight through database/sql, with no manager, no scope and no guard.
// BenchmarkTransactCommit minus this is what transaction ownership costs.
func BenchmarkStdTransactCommit(b *testing.B) {
	db := benchDB(b, newFakeDB())
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		tx, err := db.BeginTx(ctx, &stdsql.TxOptions{})
		if err != nil {
			b.Fatalf("BeginTx: %v", err)
		}
		if _, err = tx.ExecContext(ctx, benchStmt); err != nil {
			b.Fatalf("Exec: %v", err)
		}
		if err = tx.Commit(); err != nil {
			b.Fatalf("Commit: %v", err)
		}
	}
}

// BenchmarkTransactRollback is the failing path, which costs a ROLLBACK
// instead of a COMMIT and builds no error of its own — the caller's travels
// verbatim.
func BenchmarkTransactRollback(b *testing.B) {
	tm := benchTransactor(b)
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		//: the failure IS the measurement — errWork is what the benchmark
		//: sends through the rollback path, so the returned error is the
		//: expected one and is asserted rather than dropped.
		if err := tm.Transact(ctx, coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
			return errWork
		}); !errors.Is(err, errWork) {
			b.Fatalf("Transact = %v, want errWork", err)
		}
	}
}

// BenchmarkTransactNested1 adds ONE savepoint inside the transaction: the
// SAVEPOINT and RELEASE SAVEPOINT statements, a name rendered from the
// counter, and one more scope on the context chain.
func BenchmarkTransactNested1(b *testing.B) {
	benchNested(b, 1)
}

// BenchmarkTransactNested8 is the same at depth eight. The difference from
// depth one, divided by seven, is what one level of nesting costs — including
// the scopeFor walk that has to pass seven links to find its own manager.
func BenchmarkTransactNested8(b *testing.B) {
	benchNested(b, 8)
}

// benchNested runs a transaction with depth nested savepoints inside it.
func benchNested(b *testing.B, depth int) {
	b.Helper()
	tm := benchTransactor(b)
	ctx := b.Context()
	//: recursive, so each level is a real Transact on the level above it.
	var descend func(context.Context, int) error
	descend = func(c context.Context, left int) error {
		if left == 0 {
			//: the innermost scope does the one statement.
			return nil
		}
		return tm.Transact(c, coresql.TxOptionsValue{}, func(inner context.Context, ex coresql.Executor) error {
			if _, err := ex.ExecContext(inner, benchStmt); err != nil {
				return err
			}
			return descend(inner, left-1)
		})
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := descend(ctx, depth+1); err != nil {
			b.Fatalf("Transact: %v", err)
		}
	}
}

// BenchmarkScopedExecutorExec isolates the guard on the hot statement path:
// one atomic load, one mutex-protected poison read, then delegation.
func BenchmarkScopedExecutorExec(b *testing.B) {
	tm := benchTransactor(b)
	ctx := b.Context()
	b.ReportAllocs()
	err := tm.Transact(ctx, coresql.TxOptionsValue{}, func(c context.Context, ex coresql.Executor) error {
		for b.Loop() {
			if _, execErr := ex.ExecContext(c, benchStmt); execErr != nil {
				return execErr
			}
		}
		return nil
	})
	if err != nil {
		b.Fatalf("Transact: %v", err)
	}
}

// BenchmarkStdTxExec is the baseline for the guard: the same statement
// straight on a *sql.Tx.
func BenchmarkStdTxExec(b *testing.B) {
	db := benchDB(b, newFakeDB())
	ctx := b.Context()
	tx, err := db.BeginTx(ctx, &stdsql.TxOptions{})
	if err != nil {
		b.Fatalf("BeginTx: %v", err)
	}
	b.Cleanup(func() {
		//: the benchmark's transaction is abandoned deliberately; a rollback
		//: that fails still says the pool is in trouble.
		if err := tx.Rollback(); err != nil {
			b.Errorf("rolling the benchmark transaction back: %v", err)
		}
	})
	b.ReportAllocs()
	for b.Loop() {
		if _, execErr := tx.ExecContext(ctx, benchStmt); execErr != nil {
			b.Fatalf("Exec: %v", execErr)
		}
	}
}

// benchMigrations builds n valid migrations in DESCENDING order, so the
// construction benchmark measures a real sort rather than an already-ordered
// slice.
func benchMigrations(n int) []coresql.MigrationValue {
	out := make([]coresql.MigrationValue, 0, n)
	for i := n; i > 0; i-- {
		out = append(out, coresql.MigrationValue{
			Version: uint64(i), Name: "m" + strconv.Itoa(i),
			Up: svcsql.Statements("UP " + strconv.Itoa(i)), Down: coresql.Irreversible,
		})
	}
	return out
}

// BenchmarkNewMigrator measures construction: the pool policy, the clone, the
// sort, and Validate on every migration. It touches no connection.
func BenchmarkNewMigrator(b *testing.B) {
	db := benchDB(b, grantLock(newFakeDB()))
	set := benchMigrations(benchSetSize)
	b.ReportAllocs()
	for b.Loop() {
		_, err := svcsql.NewMigrator(svcsql.Config{
			DB: db, Dialect: coresql.DialectPostgres, Clock: clock.System,
			Pool: svcsql.PoolConfig{MaxOpen: 8},
		}, svcsql.MigrateConfig{Migrations: set})
		if err != nil {
			b.Fatalf("NewMigrator: %v", err)
		}
	}
}

// BenchmarkPlanEmptyHistory is the dry run on a fresh database: the version
// table read returns nothing and every migration is pending.
func BenchmarkPlanEmptyHistory(b *testing.B) {
	benchPlan(b, 0)
}

// BenchmarkPlanFullHistory is the same set with every version already
// recorded, which is what a healthy pod does on EVERY restart. It is the
// scan: benchSetSize rows decoded into the applied set, then a set difference
// that yields nothing.
func BenchmarkPlanFullHistory(b *testing.B) {
	benchPlan(b, benchSetSize)
}

// benchPlan runs Plan over benchSetSize migrations with recorded of them applied.
func benchPlan(b *testing.B, recorded int) {
	b.Helper()
	f := grantLock(newFakeDB())
	if recorded > 0 {
		versions := make([]driver.Value, 0, recorded)
		for i := 1; i <= recorded; i++ {
			versions = append(versions, int64(i))
		}
		f.rowsOn(readApplied, "version", versions...)
	}
	runner, err := svcsql.NewMigrator(svcsql.Config{
		DB: benchDB(b, f), Dialect: coresql.DialectPostgres, Clock: clock.System,
		Pool: svcsql.PoolConfig{MaxOpen: 8},
	}, svcsql.MigrateConfig{Migrations: benchMigrations(benchSetSize)})
	if err != nil {
		b.Fatalf("NewMigrator: %v", err)
	}
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		if _, planErr := runner.Plan(ctx); planErr != nil {
			b.Fatalf("Plan: %v", planErr)
		}
	}
}

// BenchmarkParseDialect is the construction-time name resolution. It is on no
// hot path at all and is measured to say so with a number.
func BenchmarkParseDialect(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := coresql.ParseDialect("postgres"); err != nil {
			b.Fatalf("ParseDialect: %v", err)
		}
	}
}

// BenchmarkMigrationValidate is the per-migration gate NewMigrator runs.
func BenchmarkMigrationValidate(b *testing.B) {
	m := coresql.MigrationValue{
		Version: 20260910143000, Name: "create accounts",
		Up: svcsql.Statements("CREATE TABLE accounts (id BIGINT PRIMARY KEY)"), Down: coresql.Irreversible,
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := m.Validate(); err != nil {
			b.Fatalf("Validate: %v", err)
		}
	}
}
