// Package queue_test — a SQL engine small enough to read, that understands
// exactly the statements the SQL broker sends and nothing else.
//
// # Why an engine and not a live database
//
// The SDK ships no driver and its service layer imports none (ADR 0055 §D2),
// so the default suite cannot open PostgreSQL, MySQL or SQLite. database/sql is
// a registry, though: this file registers a driver.Driver whose connections
// keep the queue's one table in a map, and everything above it — the pool,
// *sql.Tx, the transactor's savepoints, Join and Defer — is the real standard
// library and the real SDK. What it buys: the conformance suite on every
// dialect's statements under the race detector in the default lane, a
// statement log, PostgreSQL's aborted-transaction state, and failures
// injected at a named statement — the cases a live database will not produce
// on demand. It is docstore's fake engine (ADR 0139), for this table.
//
// What it does NOT verify is that an engine accepts the SQL, nor SKIP LOCKED:
// transactions are serialised — one writer from BEGIN to its end, as SQLite
// runs them — so no row is ever locked by another. The statement texts are
// pinned separately, and e2e/integration/sql runs the same broker on the three
// real engines, concurrency included, behind the integration tag.
package queue_test

import (
	"bytes"
	"cmp"
	"context"
	stdsql "database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// errAborted is PostgreSQL's answer to a statement after a failure in the same
// transaction, until the transaction or a savepoint rolls back.
var errAborted = errors.New("current transaction is aborted, commands ignored until end of transaction block")

// errDuplicate is the fake engine's primary-key violation. Its text quotes the
// key the way a real driver's does, so a test can prove the broker never lets
// it through.
type errDuplicate struct{ key string }

// Error quotes the key, as PostgreSQL's DETAIL and MySQL's message both do.
func (e errDuplicate) Error() string {
	return "duplicate key value violates a constraint: Key=(" + e.key + ") already exists"
}

// queueRow is one row of the queue's table: NULL is nil in every column that
// admits it.
type queueRow struct {
	lease, payload, reason, cause []byte
	code                          driver.Value // nil or int64
	id                            string
	dead, due, enqueued, count    int64
}

// queueTable is one version of the table, by identifier.
type queueTable map[string]queueRow

// clone copies t, so a transaction or a savepoint can be undone.
func (t queueTable) clone() queueTable {
	return maps.Clone(t)
}

// sqlEngine is the fake database.
type sqlEngine struct {
	// writer is held by a transaction from its BEGIN to its end, and by an
	// autocommit write for its one statement.
	writer sync.Mutex
	// mu guards everything below.
	mu sync.Mutex
	// committed is the table as every new transaction and every autocommit
	// statement sees it.
	committed queueTable
	// log records every statement's role, in order.
	log []string
	// failures fails the next execution of a role with an error.
	failures map[string][]error
	// stmts recognises what the broker sends.
	stmts svcqueue.RenderedSQL
	// dialect decides the aborted state.
	dialect coresql.Dialect
}

// newSQLEngine returns an empty engine for the broker keeping its queue in
// table on dialect.
func newSQLEngine(dialect coresql.Dialect, table string) *sqlEngine {
	return &sqlEngine{
		committed: queueTable{},
		failures:  map[string][]error{},
		stmts:     svcqueue.RenderSQLForTest(dialect, table),
		dialect:   dialect,
	}
}

// failNext makes the next execution of role fail with err.
func (e *sqlEngine) failNext(role string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.failures[role] = append(e.failures[role], err)
}

// roles returns a copy of the statement log.
func (e *sqlEngine) roles() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.log)
}

// resetLog empties the statement log.
func (e *sqlEngine) resetLog() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = nil
}

// rows returns how many rows the committed table holds.
func (e *sqlEngine) rows() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.committed)
}

// fakeSQLSeq numbers registered driver names so tests never collide in
// database/sql's process-wide registry.
var fakeSQLSeq atomic.Int64

// open registers the engine under a fresh driver name and returns a pool.
func (e *sqlEngine) open(t *testing.T) *stdsql.DB {
	t.Helper()
	name := "queuefake" + strconv.FormatInt(fakeSQLSeq.Add(1), 10)
	stdsql.Register(name, &fakeSQLDriver{engine: e})
	db, err := stdsql.Open(name, "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("closing the pool: %v", closeErr)
		}
	})
	return db
}

// fakeSQLDriver hands out connections onto one engine.
type fakeSQLDriver struct{ engine *sqlEngine }

// Open returns a new connection.
func (d *fakeSQLDriver) Open(string) (driver.Conn, error) {
	return &fakeSQLConn{engine: d.engine}, nil
}

// fakeSQLConn is one connection: autocommit, or inside its transaction.
type fakeSQLConn struct {
	engine *sqlEngine
	tx     *fakeSQLTx
}

// savepoint is one savepoint of a transaction.
type savepoint struct {
	snapshot queueTable
	name     string
}

// fakeSQLTx is one transaction's working copy and savepoints.
type fakeSQLTx struct {
	conn       *fakeSQLConn
	work       queueTable
	savepoints []savepoint
	aborted    bool
}

// Prepare is required by driver.Conn; the context-aware paths are used.
func (c *fakeSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("the fake engine takes no prepared statements")
}

// Close releases the connection.
func (c *fakeSQLConn) Close() error { return nil }

// Begin is required by driver.Conn.
func (c *fakeSQLConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

// BeginTx takes the writer and a working copy.
func (c *fakeSQLConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.engine.writer.Lock()
	e := c.engine
	e.mu.Lock()
	defer e.mu.Unlock()
	label := "BEGIN"
	if stdsql.IsolationLevel(opts.Isolation) == stdsql.LevelReadCommitted {
		label = "BEGIN READ COMMITTED"
	}
	e.log = append(e.log, label)
	c.tx = &fakeSQLTx{conn: c, work: e.committed.clone()}
	return c.tx, nil
}

// Commit publishes the working copy and releases the writer.
func (t *fakeSQLTx) Commit() error {
	e := t.conn.engine
	e.mu.Lock()
	e.log = append(e.log, "COMMIT")
	failed := t.aborted
	if !failed {
		e.committed = t.work
	}
	t.conn.tx = nil
	e.mu.Unlock()
	e.writer.Unlock()
	if failed {
		return errAborted
	}
	return nil
}

// Rollback drops the working copy and releases the writer.
func (t *fakeSQLTx) Rollback() error {
	e := t.conn.engine
	e.mu.Lock()
	e.log = append(e.log, "ROLLBACK")
	t.conn.tx = nil
	e.mu.Unlock()
	e.writer.Unlock()
	return nil
}

// answer is what one statement answers: a result set, or a count of the rows
// it affected.
type answer struct {
	cols     []string
	rows     [][]driver.Value
	affected int64
}

// ExecContext runs a statement that returns no rows.
func (c *fakeSQLConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	ans, err := c.run(ctx, query, values(args))
	if err != nil {
		return nil, err
	}
	return driver.RowsAffected(ans.affected), nil
}

// QueryContext runs a statement that returns rows.
func (c *fakeSQLConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	ans, err := c.run(ctx, query, values(args))
	if err != nil {
		return nil, err
	}
	return &fakeSQLRows{cols: ans.cols, rows: ans.rows}, nil
}

// values unwraps the named arguments.
func values(args []driver.NamedValue) []driver.Value {
	out := make([]driver.Value, len(args))
	for i, a := range args {
		out[i] = a.Value
	}
	return out
}

// run executes one statement: in the connection's transaction, or as an
// autocommit statement that takes the writer when it writes.
func (c *fakeSQLConn) run(ctx context.Context, query string, args []driver.Value) (answer, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return answer{}, ctxErr
	}
	e := c.engine
	role := e.recognise(query, len(args))
	if c.tx == nil && writes(role) {
		e.writer.Lock()
		defer e.writer.Unlock()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = append(e.log, role)
	if queue := e.failures[role]; len(queue) > 0 {
		e.failures[role] = queue[1:]
		if c.tx != nil && e.dialect == coresql.DialectPostgres {
			c.tx.aborted = true
		}
		return answer{}, queue[0]
	}
	if c.tx == nil {
		work := e.committed.clone()
		ans, err := e.execute(role, query, args, work)
		if err == nil {
			e.committed = work
		}
		return ans, err
	}
	return c.tx.run(role, query, args)
}

// writes reports whether an autocommit statement of role takes the writer.
func writes(role string) bool {
	switch role {
	case "probe", "next", "pick", "deadList":
		return false
	default:
		return true
	}
}

// run executes one statement inside the transaction.
func (t *fakeSQLTx) run(role, query string, args []driver.Value) (answer, error) {
	e := t.conn.engine
	switch {
	case strings.HasPrefix(query, "SAVEPOINT "):
		t.savepoints = append(t.savepoints, savepoint{snapshot: t.work.clone(), name: strings.TrimPrefix(query, "SAVEPOINT ")})
		return answer{}, nil
	case strings.HasPrefix(query, "ROLLBACK TO SAVEPOINT "):
		at := t.savepointAt(strings.TrimPrefix(query, "ROLLBACK TO SAVEPOINT "))
		if at < 0 {
			return answer{}, errors.New("no such savepoint")
		}
		t.work, t.aborted = t.savepoints[at].snapshot.clone(), false
		t.savepoints = t.savepoints[:at+1]
		return answer{}, nil
	case strings.HasPrefix(query, "RELEASE SAVEPOINT "):
		at := t.savepointAt(strings.TrimPrefix(query, "RELEASE SAVEPOINT "))
		if at < 0 {
			return answer{}, errors.New("no such savepoint")
		}
		t.savepoints = t.savepoints[:at]
		return answer{}, nil
	}
	if t.aborted {
		return answer{}, errAborted
	}
	ans, err := e.execute(role, query, args, t.work)
	if err != nil && e.dialect == coresql.DialectPostgres {
		t.aborted = true
	}
	return ans, err
}

// savepointAt finds the savepoint named name, or -1.
func (t *fakeSQLTx) savepointAt(name string) int {
	return slices.IndexFunc(t.savepoints, func(sp savepoint) bool { return sp.name == name })
}

// recognise names the role of a statement the broker sent, or returns the
// statement itself for one it did not (a savepoint, or a stranger).
func (e *sqlEngine) recognise(query string, nargs int) string {
	if role, ok := e.stmts.Fixed[query]; ok {
		return role
	}
	switch {
	case nargs > 2 && query == e.stmts.LeaseRows(nargs-2):
		return "leaseRows"
	case nargs > 4 && query == e.stmts.BuryRows(nargs-4):
		return "buryRows"
	}
	return query
}

// execute runs one of the broker's statements over work, the table the
// statement reads and writes.
func (e *sqlEngine) execute(role, query string, args []driver.Value, work queueTable) (answer, error) {
	switch role {
	case "insert":
		id := str(args[0])
		if _, taken := work[id]; taken {
			return answer{}, errDuplicate{key: id}
		}
		work[id] = queueRow{id: id, due: args[1].(int64), enqueued: args[2].(int64), payload: blob(args[3])}
		return answer{affected: 1}, nil
	case "lock":
		return answer{}, nil
	case "probe":
		return minDue(work, func(queueRow) bool { return true }), nil
	case "next":
		now := args[0].(int64)
		return minDue(work, func(row queueRow) bool { return row.due > now }), nil
	case "pick":
		return pick(work, args[0].(int64), int(args[1].(int64))), nil
	case "deadList":
		return deadList(work, int(args[0].(int64))), nil
	}
	return e.write(role, query, args, work)
}

// write runs the broker's statements that change the table.
func (e *sqlEngine) write(role, query string, args []driver.Value, work queueTable) (answer, error) {
	switch role {
	case "leaseRows":
		deadline, lease := args[0].(int64), blob(args[1])
		return eachLive(work, args[2:], func(row *queueRow) {
			row.due, row.lease = deadline, lease
			row.count++
		}), nil
	case "buryRows":
		return eachLive(work, args[4:], func(row *queueRow) { buryRow(row, args[:4]) }), nil
	case "ack":
		return ifHeld(work, args, func(row *queueRow) bool { return false }), nil
	case "retry":
		visibleAt := args[0].(int64)
		return ifHeld(work, args[1:], func(row *queueRow) bool {
			row.due, row.lease = visibleAt, nil
			return true
		}), nil
	case "bury":
		return ifHeld(work, args[4:], func(row *queueRow) bool {
			buryRow(row, args[:4])
			return true
		}), nil
	case "extend":
		deadline, lease := args[0].(int64), blob(args[1])
		return ifHeld(work, args[2:], func(row *queueRow) bool {
			row.due, row.lease = deadline, lease
			return true
		}), nil
	case "replay":
		return ifDead(work, str(args[1]), func(row *queueRow) bool {
			row.dead, row.due, row.count = 0, args[0].(int64), 0
			row.reason, row.cause, row.code = nil, nil, nil
			return true
		}), nil
	case "deleteDead":
		return ifDead(work, str(args[0]), func(*queueRow) bool { return false }), nil
	}
	return answer{}, errors.New("the fake engine does not speak: " + query)
}

// buryRow makes row a dead letter with the instant and cause in args: the
// instant, the reason, the cause and the code.
func buryRow(row *queueRow, args []driver.Value) {
	row.dead, row.due, row.lease = 1, args[0].(int64), nil
	row.reason, row.cause, row.code = blob(args[1]), blob(args[2]), args[3]
}

// eachLive applies change to every live row among the identifiers ids and
// counts them.
func eachLive(work queueTable, ids []driver.Value, change func(*queueRow)) answer {
	n := int64(0)
	for _, id := range ids {
		row, found := work[str(id)]
		if !found || row.dead != 0 {
			continue
		}
		change(&row)
		work[row.id] = row
		n++
	}
	return answer{affected: n}
}

// ifHeld applies change to the row the four held arguments name — the
// identifier, the lease, the count, the instant — when its lease still holds;
// change reports whether the row stays, and a row that does not is deleted.
func ifHeld(work queueTable, held []driver.Value, change func(*queueRow) bool) answer {
	row, found := work[str(held[0])]
	if !found || row.dead != 0 || row.lease == nil || !bytes.Equal(row.lease, blob(held[1])) ||
		row.count != held[2].(int64) || row.due <= held[3].(int64) {
		return answer{}
	}
	if change(&row) {
		work[row.id] = row
	} else {
		delete(work, row.id)
	}
	return answer{affected: 1}
}

// ifDead applies change to the dead letter id; change reports whether the row
// stays, and a row that does not is deleted.
func ifDead(work queueTable, id string, change func(*queueRow) bool) answer {
	row, found := work[id]
	if !found || row.dead != 1 {
		return answer{}
	}
	if change(&row) {
		work[row.id] = row
	} else {
		delete(work, row.id)
	}
	return answer{affected: 1}
}

// minDue answers MIN(due) over the live rows keep accepts: NULL for none.
func minDue(work queueTable, keep func(queueRow) bool) answer {
	var least driver.Value
	for _, row := range work {
		if row.dead == 0 && keep(row) && (least == nil || row.due < least.(int64)) {
			least = row.due
		}
	}
	return answer{cols: []string{"min"}, rows: [][]driver.Value{{least}}}
}

// ordered returns the rows of work keep accepts, by (due, id).
func ordered(work queueTable, keep func(queueRow) bool) []queueRow {
	var out []queueRow
	for _, row := range work {
		if keep(row) {
			out = append(out, row)
		}
	}
	slices.SortFunc(out, func(a, b queueRow) int {
		return cmp.Or(cmp.Compare(a.due, b.due), cmp.Compare(a.id, b.id))
	})
	return out
}

// pick answers the live rows due by now, oldest due first, at most limit.
func pick(work queueTable, now int64, limit int) answer {
	due := ordered(work, func(row queueRow) bool { return row.dead == 0 && row.due <= now })
	rows := make([][]driver.Value, 0, min(limit, len(due)))
	for _, row := range due[:min(limit, len(due))] {
		rows = append(rows, []driver.Value{[]byte(row.id), row.enqueued, row.count, nullable(row.lease), nullable(row.payload)})
	}
	return answer{cols: []string{"id", "enqueued_at", "deliveries", "lease", "payload"}, rows: rows}
}

// deadList answers the dead letters in the order they died, at most limit.
func deadList(work queueTable, limit int) answer {
	dead := ordered(work, func(row queueRow) bool { return row.dead == 1 })
	rows := make([][]driver.Value, 0, min(limit, len(dead)))
	for _, row := range dead[:min(limit, len(dead))] {
		rows = append(rows, []driver.Value{
			[]byte(row.id), row.enqueued, row.count, row.due,
			nullable(row.reason), nullable(row.cause), row.code, nullable(row.payload),
		})
	}
	return answer{cols: []string{"id", "enqueued_at", "deliveries", "due", "reason", "cause", "code", "payload"}, rows: rows}
}

// str reads a bound []byte as a string.
func str(v driver.Value) string {
	return string(v.([]byte))
}

// blob reads a bound []byte, nil for NULL, as a copy.
func blob(v driver.Value) []byte {
	b, _ := v.([]byte)
	if b == nil {
		return nil
	}
	return bytes.Clone(b)
}

// nullable is b as a column value: nil, which is NULL, when b is nil.
func nullable(b []byte) driver.Value {
	if b == nil {
		return nil
	}
	return bytes.Clone(b)
}

// fakeSQLRows replays a result set.
type fakeSQLRows struct {
	cols []string
	rows [][]driver.Value
	next int
}

// Columns names the result columns.
func (r *fakeSQLRows) Columns() []string { return r.cols }

// Close releases the result set.
func (r *fakeSQLRows) Close() error { return nil }

// Next copies the next row, or reports the end of the set.
func (r *fakeSQLRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}
