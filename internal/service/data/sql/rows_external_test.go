// Package sql_test — #837's four pieces: Each, InsertRows, the statement
// budget and the read replica.
package sql_test

import (
	"context"
	stdsql "database/sql"
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"
)

// scanInt reads one integer column.
func scanInt(rows *stdsql.Rows) (int64, error) {
	var n int64
	err := rows.Scan(&n)
	return n, err
}

// threeRows scripts query to answer the integers 1, 2 and 3.
func threeRows(f *fakeDB, query string) *fakeDB {
	return f.rowsOn(query, "n", int64(1), int64(2), int64(3))
}

// TestEachClosesTheRowsWhenTheLoopStopsEarly is the first half of the first
// acceptance: a loop that breaks after one row leaves no result set open,
// counted on the driver double.
func TestEachClosesTheRowsWhenTheLoopStopsEarly(t *testing.T) {
	t.Parallel()
	f := threeRows(newFakeDB(), "SELECT n FROM t")
	db := closeOnCleanup(t, f.open())
	rows, err := db.QueryContext(t.Context(), "SELECT n FROM t")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	var seen []int64
	for n, err := range svcsql.Each(rows, scanInt) {
		if err != nil {
			t.Fatalf("Each yielded %v", err)
		}
		seen = append(seen, n)
		break
	}
	if !slices.Equal(seen, []int64{1}) {
		t.Fatalf("seen %v, want the first row", seen)
	}
	if opened, closed := f.openRows(); opened != 1 || closed != 1 {
		t.Fatalf("rows opened %d, closed %d: an early stop left a result set open", opened, closed)
	}
}

// TestEachYieldsEveryRowThenAFailureOnce pins the complete read and the
// failure path: every row in order, and a scan's failure as the last element,
// beside the zero value, the rows closed.
func TestEachYieldsEveryRowThenAFailureOnce(t *testing.T) {
	t.Parallel()
	f := threeRows(newFakeDB(), "SELECT n FROM t")
	db := closeOnCleanup(t, f.open())
	rows, err := db.QueryContext(t.Context(), "SELECT n FROM t")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	var seen []int64
	for n, err := range svcsql.Each(rows, scanInt) {
		if err != nil {
			t.Fatalf("Each yielded %v", err)
		}
		seen = append(seen, n)
	}
	if !slices.Equal(seen, []int64{1, 2, 3}) {
		t.Fatalf("seen %v, want every row in order", seen)
	}
	rows, err = db.QueryContext(t.Context(), "SELECT n FROM t")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	failures := 0
	for n, err := range svcsql.Each(rows, func(*stdsql.Rows) (int64, error) { return 9, errWork }) {
		if !errors.Is(err, errWork) || n != 0 {
			t.Fatalf("Each yielded (%d, %v), want (0, the scan's failure)", n, err)
		}
		failures++
	}
	if failures != 1 {
		t.Fatalf("the failure was yielded %d times, want once", failures)
	}
	if opened, closed := f.openRows(); opened != 2 || closed != 2 {
		t.Fatalf("rows opened %d, closed %d", opened, closed)
	}
	for range svcsql.Each[int64](nil, scanInt) {
		t.Fatal("a nil rows yielded")
	}
}

// TestInsertRowsChunksUnderTheEngineCeiling pins the statement InsertRows
// renders — the shape docstore's index rows use, byte for byte — and that a
// chunk never binds more than the engine takes: 999 parameters on SQLite.
func TestInsertRowsChunksUnderTheEngineCeiling(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	db := closeOnCleanup(t, f.open())
	cols := []string{"index_name", "index_key", "doc_key", "uniq"}
	rows := [][]any{{"a", "1", "k", false}, {"b", "2", "k", true}, {"c", "3", "k", false}}
	if _, err := svcsql.InsertRows(t.Context(), db, coresql.DialectPostgres, svcsql.InsertSpec{Table: `"ix"`, Columns: cols, PerStatement: 2}, rows); err != nil {
		t.Fatalf("InsertRows: %v", err)
	}
	want := []string{
		`INSERT INTO "ix" (index_name, index_key, doc_key, uniq) VALUES ($1, $2, $3, $4), ($5, $6, $7, $8)`,
		`INSERT INTO "ix" (index_name, index_key, doc_key, uniq) VALUES ($1, $2, $3, $4)`,
	}
	if got := f.statements(); !slices.Equal(got, want) {
		t.Fatalf("statements = %q, want %q", got, want)
	}
	lite := newFakeDB()
	ldb := closeOnCleanup(t, lite.open())
	many := make([][]any, 300)
	for i := range many {
		many[i] = []any{"a", "b", "c", "d"}
	}
	if _, err := svcsql.InsertRows(t.Context(), ldb, coresql.DialectSQLite, svcsql.InsertSpec{Table: "ix", Columns: cols}, many); err != nil {
		t.Fatalf("InsertRows: %v", err)
	}
	log := lite.statements()
	if len(log) != 2 {
		t.Fatalf("SQLite sent %d statements for 300 rows of 4, want 2 (249 rows, then 51)", len(log))
	}
	if markers := strings.Count(log[0], "?"); markers != 996 {
		t.Fatalf("the first SQLite statement binds %d parameters, want 996 — at most 999", markers)
	}
}

// TestInsertRowsRefusesARowOfTheWrongWidth pins the refusals made before any
// statement runs: a row whose width is not the columns', no column, an unset
// dialect.
func TestInsertRowsRefusesARowOfTheWrongWidth(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	db := closeOnCleanup(t, f.open())
	for name, call := range map[string]func() error{
		"width": func() error {
			_, err := svcsql.InsertRows(t.Context(), db, coresql.DialectMySQL, svcsql.InsertSpec{Table: "t", Columns: []string{"a", "b"}},
				[][]any{{1, 2}, {3}})
			return err
		},
		"columns": func() error {
			_, err := svcsql.InsertRows(t.Context(), db, coresql.DialectMySQL, svcsql.InsertSpec{Table: "t"}, [][]any{{1}})
			return err
		},
		"dialect": func() error {
			_, err := svcsql.InsertRows(t.Context(), db, coresql.DialectUnknown, svcsql.InsertSpec{Table: "t", Columns: []string{"a"}}, [][]any{{1}})
			return err
		},
	} {
		if err := call(); !errs.HasCode(err, coresql.CodeConfigInvalid) {
			t.Errorf("%s: InsertRows = %v, want CONFIG_INVALID", name, err)
		}
	}
	if got := f.statements(); len(got) != 0 {
		t.Fatalf("statements = %v, want none", got)
	}
}

// budgeted is a transactor over f whose statements are bounded by budget on
// clk, with replica's pool for its reads when replica is not nil.
func budgeted(t *testing.T, f, replica *fakeDB, clk *clock.ManualClock, budget time.Duration) coresql.Transactor {
	t.Helper()
	cfg := svcsql.Config{
		DB: closeOnCleanup(t, f.open()), Dialect: coresql.DialectPostgres,
		Pool: svcsql.PoolConfig{MaxOpen: 4}, StatementTimeout: budget,
	}
	if clk != nil {
		cfg.Clock = clk
	}
	if replica != nil {
		cfg.ReadReplica = closeOnCleanup(t, replica.open())
	}
	tm, err := svcsql.NewTransactor(cfg)
	if err != nil {
		t.Fatalf("NewTransactor: %v", err)
	}
	return tm
}

// TestAStatementPastItsBudgetIsCutAndNamed is the second acceptance's first
// half: a statement that outlives StatementTimeout on the driver double
// answers STATEMENT_TIMED_OUT once the manual clock reaches the budget — in a
// transaction, and through Join on the pool — its operation named.
func TestAStatementPastItsBudgetIsCutAndNamed(t *testing.T) {
	t.Parallel()
	f := newFakeDB().hang("SELECT pg_sleep(60)").hang("UPDATE slow")
	clk := manualClock()
	tm := budgeted(t, f, nil, clk, time.Second)
	inTx := make(chan error, 1)
	go func() {
		inTx <- tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, ex coresql.Executor) error {
			_, err := ex.ExecContext(ctx, "UPDATE slow")
			return err
		})
	}()
	clk.BlockUntil(1)
	clk.Advance(time.Second)
	err := <-inTx
	if !errs.HasCode(err, coresql.CodeStatementTimedOut) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Transact = %v, want STATEMENT_TIMED_OUT beside the context's end", err)
	}
	if !hasFieldValue(verdictOf(err, coresql.CodeStatementTimedOut), "operation", "exec") {
		t.Fatalf("Transact = %v, want operation=exec", err)
	}
	pool, inTxNow := tm.(coresql.Joiner).Join(t.Context())
	if inTxNow {
		t.Fatal("Join outside a transaction says inside one")
	}
	onPool := make(chan error, 1)
	go func() {
		_, err := pool.QueryContext(t.Context(), "SELECT pg_sleep(60)")
		onPool <- err
	}()
	clk.BlockUntil(1)
	clk.Advance(time.Second)
	if err := <-onPool; !hasFieldValue(verdictOf(err, coresql.CodeStatementTimedOut), "operation", "query") {
		t.Fatalf("a query on the pool = %v, want STATEMENT_TIMED_OUT with operation=query", err)
	}
}

// TestAStatementInTimeIsNotCutAndNoBudgetIsTheCallersPool pins the other side:
// a statement that returns within its budget is untouched and leaves no wait
// armed, and with no budget Join answers the caller's own *sql.DB.
func TestAStatementInTimeIsNotCutAndNoBudgetIsTheCallersPool(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	clk := manualClock()
	tm := budgeted(t, f, nil, clk, time.Second)
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, ex coresql.Executor) error {
		_, err := ex.ExecContext(ctx, "UPDATE fast")
		return err
	})
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}
	unbounded := budgeted(t, newFakeDB(), nil, nil, 0)
	if ex, _ := unbounded.(coresql.Joiner).Join(t.Context()); func() bool { _, ok := ex.(*stdsql.DB); return !ok }() {
		t.Fatalf("Join with no budget = %T, want the caller's *sql.DB", ex)
	}
}

// TestAReadOnlyTransactionRunsOnTheReplica is the second acceptance's other
// half: a ReadOnly Transact begins on the replica's pool — the double records
// which served it —, a read-write one on the primary's, and Reader routes a
// read outside a transaction to the replica while Join stays on the primary.
func TestAReadOnlyTransactionRunsOnTheReplica(t *testing.T) {
	t.Parallel()
	primary, replica := newFakeDB(), newFakeDB()
	tm := budgeted(t, primary, replica, nil, 0)
	read := func(ctx context.Context, ex coresql.Executor) error {
		_, err := ex.ExecContext(ctx, "SELECT 1")
		return err
	}
	if err := tm.Transact(t.Context(), coresql.TxOptionsValue{ReadOnly: true}, read); err != nil {
		t.Fatalf("read-only Transact: %v", err)
	}
	if err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, read); err != nil {
		t.Fatalf("Transact: %v", err)
	}
	want := []string{"BEGIN", "SELECT 1", "COMMIT"}
	if got := replica.statements(); !slices.Equal(got, want) {
		t.Fatalf("replica served %v, want the read-only transaction %v", got, want)
	}
	if got := primary.statements(); !slices.Equal(got, want) {
		t.Fatalf("primary served %v, want the read-write transaction %v", got, want)
	}
	if _, err := tm.(coresql.Router).Reader(t.Context()).ExecContext(t.Context(), "SELECT 2"); err != nil {
		t.Fatalf("Reader: %v", err)
	}
	if !replica.sent("SELECT 2") || primary.sent("SELECT 2") {
		t.Fatal("a read outside a transaction did not go to the replica")
	}
	ex, _ := tm.(coresql.Joiner).Join(t.Context())
	if _, err := ex.ExecContext(t.Context(), "UPDATE 3"); err != nil || !primary.sent("UPDATE 3") {
		t.Fatalf("Join outside a transaction did not stay on the primary: %v", err)
	}
}

// TestTheProbePingsTheReplicaToo pins that a replica that does not answer
// fails the health check, as the primary does.
func TestTheProbePingsTheReplicaToo(t *testing.T) {
	t.Parallel()
	primary, replica := newFakeDB(), newFakeDB()
	replica.pingErr = driver.ErrBadConn
	probe, err := svcsql.NewChecker(svcsql.Config{
		DB: closeOnCleanup(t, primary.open()), ReadReplica: closeOnCleanup(t, replica.open()),
		Dialect: coresql.DialectPostgres, Pool: svcsql.PoolConfig{MaxOpen: 2},
	})
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	if err := probe.Check(t.Context()); !errs.HasCode(err, coresql.CodeHealthCheckFailed) {
		t.Fatalf("Check = %v, want HEALTH_CHECK_FAILED for the replica", err)
	}
}
