// Package sql_test — hosts the fake database/sql driver every test in this
// package runs against, and the small helpers built on it.
//
// # Why a fake driver and not a real database
//
// database/sql is a REGISTRY: sql.Register hands the standard library a
// driver.Driver, and everything above it — the pool, the transaction, the
// context plumbing, ErrTxDone — is the real, unmodified standard library. So
// a scripted driver exercises the actual code path a production deployment
// takes, minus the network and the server.
//
// That buys three things this package needs and a live database cannot give:
// a statement LOG, so a test asserts the exact SQL sent and its order (which
// is the whole contract of a savepoint implementation); deterministic
// FAILURES on a named statement, so "what happens when ROLLBACK TO SAVEPOINT
// itself fails" is a test rather than a paragraph; and no container, so the
// suite runs in the sandbox with the race detector on.
//
// What it deliberately does NOT verify is that PostgreSQL accepts the SQL —
// that is a conformance question for e2e, and it is stated rather than
// implied.
package sql_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	stdsql "database/sql"
)

// errScripted is the failure a scripted statement returns when the test asks
// for one without supplying a specific error.
var errScripted = errors.New("scripted failure")

// responder answers one statement. call is 0-based, so a script can answer
// differently on the second attempt — which is how the advisory-lock retry is
// tested.
type responder func(call int) (cols []string, rows [][]driver.Value, err error)

// fakeDB is the scripted server every fake connection talks to. One instance
// is shared by every connection of one *sql.DB, so the statement log is
// ordered across the whole pool.
type fakeDB struct {
	// mu guards every field below.
	mu sync.Mutex
	// log records every statement in the order the driver received it.
	log []string
	// scripts answers specific statements; anything unscripted succeeds with
	// an empty result.
	scripts map[string]responder
	// calls counts how many times each statement has been executed, so a
	// responder can answer differently the second time.
	calls map[string]int
	// beginErr, commitErr, rollbackErr fail the transaction verbs.
	beginErr    error
	commitErr   error
	rollbackErr error
	// pingErr fails the liveness probe.
	pingErr error
	// pingBlock, when non-nil, blocks every ping until it is closed or the
	// probe's context is cancelled.
	pingBlock chan struct{}
	// openConns counts connections handed out and not yet closed, so a test
	// can prove the migration lock does not leak one.
	openConns int
	// quiet suppresses the statement log. A benchmark runs the same
	// statement millions of times, and appending each one would measure the
	// harness's own growing slice instead of the code under test.
	quiet bool
}

// newFakeDB returns an empty scripted server.
func newFakeDB() *fakeDB {
	return &fakeDB{scripts: map[string]responder{}, calls: map[string]int{}}
}

// on scripts an exact statement.
func (f *fakeDB) on(query string, answer responder) *fakeDB {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts[query] = answer
	return f
}

// failOn scripts an exact statement to fail with errScripted.
func (f *fakeDB) failOn(query string) *fakeDB {
	return f.on(query, func(int) ([]string, [][]driver.Value, error) {
		return nil, nil, errScripted
	})
}

// rowsOn scripts an exact statement to return one column of values.
func (f *fakeDB) rowsOn(query, col string, values ...driver.Value) *fakeDB {
	return f.on(query, func(int) ([]string, [][]driver.Value, error) {
		out := make([][]driver.Value, 0, len(values))
		for _, v := range values {
			out = append(out, []driver.Value{v})
		}
		return []string{col}, out, nil
	})
}

// statements returns a copy of the statement log.
func (f *fakeDB) statements() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.log)
}

// sent reports whether the log contains an exact statement.
func (f *fakeDB) sent(query string) bool {
	return slices.Contains(f.statements(), query)
}

// live reports how many connections are open.
func (f *fakeDB) live() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.openConns
}

// silent returns f with the statement log switched off, for benchmarks.
func (f *fakeDB) silent() *fakeDB {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.quiet = true
	return f
}

// run records a statement and returns its scripted answer.
func (f *fakeDB) run(query string) ([]string, [][]driver.Value, error) {
	f.mu.Lock()
	if !f.quiet {
		f.log = append(f.log, query)
	}
	answer, scripted := f.scripts[query]
	call := f.calls[query]
	f.calls[query] = call + 1
	f.mu.Unlock()
	if !scripted {
		return nil, nil, nil
	}
	return answer(call)
}

// driverSeq numbers registered driver names so two tests never collide in the
// process-wide sql.Register registry.
var driverSeq atomic.Int64

// open registers f under a fresh driver name and returns a *sql.DB over it.
func (f *fakeDB) open() *stdsql.DB {
	name := "ktnfake" + strconv.FormatInt(driverSeq.Add(1), 10)
	stdsql.Register(name, &fakeDriver{db: f})
	db, err := stdsql.Open(name, "")
	if err != nil {
		panic(err)
	}
	return db
}

// closeOnCleanup registers db for close at the end of the test and REPORTS a
// close failure rather than discarding it.
//
// Generic over io.Closer rather than taking *sql.DB: it needs exactly one
// method, and the type parameter is what lets it hand the caller back the
// concrete pool it was given.
//
// Every test in this package opens a pool, and `t.Cleanup(func() { _ = db.Close() })`
// repeated fifteen times is fifteen places a real failure could hide. One
// helper makes the check free at the call site.
func closeOnCleanup[T io.Closer](tb testing.TB, db T) T {
	tb.Helper()
	tb.Cleanup(func() {
		//: a pool that will not close is a leaked connection set, and in this
		//: package it is also the failure mode the migration-lock tests exist
		//: to catch.
		if err := db.Close(); err != nil {
			tb.Errorf("closing the pool: %v", err)
		}
	})
	return db
}

// verdictIgnored records the outcome of a call whose VERDICT is not what the
// test is asserting — the statement log is.
//
// It logs rather than discards, so a surprising change shows up under
// `go test -v` instead of vanishing into an underscore. Several tests here
// drive a transaction purely to observe the SQL it sends; whether it
// ultimately committed is the subject of the transaction suite, not of them.
func verdictIgnored(tb testing.TB, err error) {
	tb.Helper()
	//: nil is the uninteresting majority and says nothing worth a line.
	if err != nil {
		tb.Logf("verdict not asserted by this test: %v", err)
	}
}

// fakeDriver hands out connections onto one fakeDB.
type fakeDriver struct{ db *fakeDB }

// Open returns a new connection to the scripted server.
func (d *fakeDriver) Open(string) (driver.Conn, error) {
	d.db.mu.Lock()
	d.db.openConns++
	d.db.mu.Unlock()
	return &fakeConn{db: d.db}, nil
}

// fakeConn is one connection. It implements the context-aware interfaces so
// database/sql never falls back to Prepare.
type fakeConn struct{ db *fakeDB }

// Prepare compiles a statement. Present because driver.Conn requires it.
func (c *fakeConn) Prepare(query string) (driver.Stmt, error) {
	return &fakeStmt{conn: c, query: query}, nil
}

// Close releases the connection.
func (c *fakeConn) Close() error {
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	c.db.openConns--
	return nil
}

// Begin opens a transaction. Deprecated in database/sql but required by the
// interface.
func (c *fakeConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

// BeginTx opens a transaction, honouring the scripted begin failure.
func (c *fakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.db.mu.Lock()
	failure := c.db.beginErr
	if !c.db.quiet {
		c.db.log = append(c.db.log, "BEGIN")
	}
	c.db.mu.Unlock()
	if failure != nil {
		return nil, failure
	}
	return &fakeTx{db: c.db}, nil
}

// ExecContext runs a statement that returns no rows.
func (c *fakeConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	_, _, err := c.db.run(query)
	if err != nil {
		return nil, err
	}
	return driver.RowsAffected(0), nil
}

// QueryContext runs a statement that returns rows.
func (c *fakeConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	cols, rows, err := c.db.run(query)
	if err != nil {
		return nil, err
	}
	if cols == nil {
		cols = []string{"c"}
	}
	return &fakeRows{cols: cols, rows: rows}, nil
}

// Ping answers the liveness probe, honouring the scripted block and failure.
func (c *fakeConn) Ping(ctx context.Context) error {
	c.db.mu.Lock()
	block, failure := c.db.pingBlock, c.db.pingErr
	c.db.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return failure
}

// fakeStmt is the minimal prepared statement.
type fakeStmt struct {
	conn  *fakeConn
	query string
}

// Close releases the statement.
func (s *fakeStmt) Close() error { return nil }

// NumInput reports that the statement accepts any number of arguments.
func (s *fakeStmt) NumInput() int { return -1 }

// Exec runs the statement.
func (s *fakeStmt) Exec([]driver.Value) (driver.Result, error) {
	return s.conn.ExecContext(context.Background(), s.query, nil)
}

// Query runs the statement.
func (s *fakeStmt) Query([]driver.Value) (driver.Rows, error) {
	return s.conn.QueryContext(context.Background(), s.query, nil)
}

// fakeTx is one transaction on the scripted server.
type fakeTx struct{ db *fakeDB }

// Commit ends the transaction, honouring the scripted commit failure.
func (t *fakeTx) Commit() error {
	t.db.mu.Lock()
	defer t.db.mu.Unlock()
	if !t.db.quiet {
		t.db.log = append(t.db.log, "COMMIT")
	}
	return t.db.commitErr
}

// Rollback undoes the transaction, honouring the scripted rollback failure.
func (t *fakeTx) Rollback() error {
	t.db.mu.Lock()
	defer t.db.mu.Unlock()
	if !t.db.quiet {
		t.db.log = append(t.db.log, "ROLLBACK")
	}
	return t.db.rollbackErr
}

// fakeRows replays a scripted result set.
type fakeRows struct {
	cols []string
	rows [][]driver.Value
	next int
}

// Columns names the result columns.
func (r *fakeRows) Columns() []string { return r.cols }

// Close releases the result set.
func (r *fakeRows) Close() error { return nil }

// Next copies the next row, or reports the end of the set.
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}
