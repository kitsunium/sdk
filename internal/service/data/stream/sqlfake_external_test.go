// Package stream_test — a SQL engine small enough to read, that understands
// exactly the statements the SQL stream sends and nothing else.
//
// # Why an engine and not a live database
//
// The SDK ships no driver and its service layer imports none (ADR 0055 §D2),
// so the default suite cannot open PostgreSQL, MySQL or SQLite. database/sql
// is a registry, though: this file registers a driver.Driver whose
// connections keep the stream's three tables in maps, and everything above it
// — the pool, *sql.Tx, the transactor's savepoints, Join and Defer — is the
// real standard library and the real SDK. It is the queue's fake engine
// (internal/service/data/queue/sqlfake_external_test.go), for these tables.
//
// What it does NOT verify is that an engine accepts the SQL, nor the row
// lock: transactions are serialised — one writer from BEGIN to its end, as
// SQLite runs them. The statement texts are pinned separately, and
// e2e/integration/sql runs the same engine on the three real engines,
// concurrent appenders included, behind the integration tag.
package stream_test

import (
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
	svcstream "github.com/kitsunium/sdk/internal/service/data/stream"
)

// errAborted is PostgreSQL's answer to a statement after a failure in the same
// transaction, until the transaction or a savepoint rolls back.
var errAborted = errors.New("current transaction is aborted, commands ignored until end of transaction block")

// recordKey is a record's primary key.
type recordKey struct{ part, offset int64 }

// recordRow is one stored record.
type recordRow struct {
	key, payload []byte
	at           int64
}

// headRow is one partition's head.
type headRow struct{ next, first, last int64 }

// streamTables is one version of the three tables.
type streamTables struct {
	records map[recordKey]recordRow
	heads   map[int64]headRow
	cursors map[string]int64
}

// clone copies t, so a transaction or a savepoint can be undone.
func (t streamTables) clone() streamTables {
	return streamTables{records: maps.Clone(t.records), heads: maps.Clone(t.heads), cursors: maps.Clone(t.cursors)}
}

// sqlEngine is the fake database.
type sqlEngine struct {
	// writer is held by a transaction from its BEGIN to its end, and by an
	// autocommit write for its one statement.
	writer sync.Mutex
	// mu guards everything below.
	mu        sync.Mutex
	committed streamTables
	// log records every statement's role, in order.
	log []string
	// failures fails the next execution of a role with an error.
	failures map[string][]error
	stmts    svcstream.RenderedSQL
	dialect  coresql.Dialect
}

// newSQLEngine returns an empty engine for the stream keeping its records in
// table on dialect.
func newSQLEngine(dialect coresql.Dialect, table string) *sqlEngine {
	return &sqlEngine{
		committed: streamTables{records: map[recordKey]recordRow{}, heads: map[int64]headRow{}, cursors: map[string]int64{}},
		failures:  map[string][]error{},
		stmts:     svcstream.RenderSQLForTest(dialect, table),
		dialect:   dialect,
	}
}

// failNext makes the next execution of role fail with err.
func (e *sqlEngine) failNext(role string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.failures[role] = append(e.failures[role], err)
}

// statements returns the roles of the stream's statements logged since the
// last reset, leaving out the transaction control the transactor sends.
func (e *sqlEngine) statements() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(e.log), func(role string) bool {
		return strings.HasPrefix(role, "BEGIN") || role == "COMMIT" || role == "ROLLBACK" ||
			strings.Contains(role, "SAVEPOINT ")
	})
}

// resetLog empties the statement log.
func (e *sqlEngine) resetLog() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = nil
}

// rows returns how many records the committed table stores.
func (e *sqlEngine) rows() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.committed.records)
}

// fakeSQLSeq numbers registered driver names so tests never collide in
// database/sql's process-wide registry.
var fakeSQLSeq atomic.Int64

// open registers the engine under a fresh driver name and returns a pool.
func (e *sqlEngine) open(t testing.TB) *stdsql.DB {
	t.Helper()
	name := "streamfake" + strconv.FormatInt(fakeSQLSeq.Add(1), 10)
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
	snapshot streamTables
	name     string
}

// fakeSQLTx is one transaction's working copy and savepoints.
type fakeSQLTx struct {
	conn       *fakeSQLConn
	work       streamTables
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
	width := 0
	if len(ans.rows) > 0 {
		width = len(ans.rows[0])
	}
	cols := make([]string, width)
	for i := range cols {
		cols[i] = "c" + strconv.Itoa(i)
	}
	return &fakeSQLRows{cols: cols, rows: ans.rows}, nil
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
		ans, err := execute(role, args, work)
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
	case "head", "read", "bounds", "offsetAt", "lowest", "headNext", "committed":
		return false
	default:
		return true
	}
}

// run executes one statement inside the transaction.
func (t *fakeSQLTx) run(role, query string, args []driver.Value) (answer, error) {
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
	ans, err := execute(role, args, t.work)
	if err != nil && t.conn.engine.dialect == coresql.DialectPostgres {
		t.aborted = true
	}
	return ans, err
}

// savepointAt finds the savepoint named name, or -1.
func (t *fakeSQLTx) savepointAt(name string) int {
	return slices.IndexFunc(t.savepoints, func(sp savepoint) bool { return sp.name == name })
}

// recognise names the role of a statement the stream sent, or returns the
// statement itself for one it did not (a savepoint, or a stranger).
func (e *sqlEngine) recognise(query string, nargs int) string {
	if role, ok := e.stmts.Fixed[query]; ok {
		return role
	}
	if nargs > 0 && nargs%5 == 0 && query == e.stmts.InsertRows(nargs/5) {
		return "insertRows"
	}
	return query
}

// execute runs one of the stream's statements over work.
func execute(role string, args []driver.Value, work streamTables) (answer, error) {
	switch role {
	case "lockHead":
		part := args[0].(int64)
		if _, found := work.heads[part]; !found {
			work.heads[part] = headRow{}
		}
		return answer{affected: 1}, nil
	case "head", "headNext":
		head, found := work.heads[args[0].(int64)]
		if !found {
			return answer{}, nil
		}
		if role == "headNext" {
			return answer{rows: [][]driver.Value{{head.next}}}, nil
		}
		return answer{rows: [][]driver.Value{{head.next, head.first, head.last}}}, nil
	case "insertRows":
		for i := 0; i < len(args); i += 5 {
			key := recordKey{part: args[i].(int64), offset: args[i+1].(int64)}
			if _, taken := work.records[key]; taken {
				return answer{}, errors.New("duplicate key value violates unique constraint")
			}
			work.records[key] = recordRow{at: args[i+2].(int64), key: blob(args[i+3]), payload: blob(args[i+4])}
		}
		return answer{affected: int64(len(args) / 5)}, nil
	case "advance":
		part := args[2].(int64)
		head := work.heads[part]
		head.next, head.last = args[0].(int64), args[1].(int64)
		work.heads[part] = head
		return answer{affected: 1}, nil
	case "floor":
		part := args[1].(int64)
		head := work.heads[part]
		head.first = args[0].(int64)
		work.heads[part] = head
		return answer{affected: 1}, nil
	case "read":
		return read(work, args), nil
	case "bounds":
		return lowestReadable(work, args[1].(int64), args[0].(int64), 0), nil
	case "offsetAt":
		return lowestReadable(work, args[2].(int64), args[0].(int64), args[1].(int64)), nil
	case "lowest":
		var low driver.Value
		for key := range work.records {
			if key.part == args[0].(int64) && (low == nil || key.offset < low.(int64)) {
				low = key.offset
			}
		}
		return answer{rows: [][]driver.Value{{low}}}, nil
	case "release":
		var n int64
		for key := range work.records {
			if key.part == args[0].(int64) && key.offset < args[1].(int64) {
				delete(work.records, key)
				n++
			}
		}
		return answer{affected: n}, nil
	case "commit":
		work.cursors[string(blob(args[0]))+"/"+strconv.FormatInt(args[1].(int64), 10)] = args[2].(int64)
		return answer{affected: 1}, nil
	case "committed":
		next, found := work.cursors[string(blob(args[0]))+"/"+strconv.FormatInt(args[1].(int64), 10)]
		if !found {
			return answer{}, nil
		}
		return answer{rows: [][]driver.Value{{next}}}, nil
	}
	return answer{}, errors.New("the fake engine does not know this statement: " + role)
}

// readable returns partition's records a read sees: at or above its floor
// and appended after cutoff, in offset order.
func readable(work streamTables, part, cutoff int64) []recordKey {
	head := work.heads[part]
	var keys []recordKey
	for key, row := range work.records {
		if key.part == part && key.offset >= head.first && row.at > cutoff {
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, func(a, b recordKey) int { return cmp.Compare(a.offset, b.offset) })
	return keys
}

// read answers the read statement: the head's next offset on every row, the
// records from an offset on, or one row of NULLs.
func read(work streamTables, args []driver.Value) answer {
	cutoff, from, part, limit := args[0].(int64), args[1].(int64), args[2].(int64), int(args[3].(int64))
	head, found := work.heads[part]
	if !found {
		return answer{}
	}
	var rows [][]driver.Value
	for _, key := range readable(work, part, cutoff) {
		if key.offset < from || len(rows) == limit {
			continue
		}
		row := work.records[key]
		rows = append(rows, []driver.Value{head.next, key.offset, row.at, nullable(row.key), nullable(row.payload)})
	}
	if len(rows) == 0 {
		rows = [][]driver.Value{{head.next, nil, nil, nil, nil}}
	}
	return answer{rows: rows}
}

// lowestReadable answers the bounds and offsetAt statements: the head's next
// offset and the lowest readable offset appended at or after at.
func lowestReadable(work streamTables, part, cutoff, at int64) answer {
	head, found := work.heads[part]
	if !found {
		return answer{}
	}
	var low driver.Value
	for _, key := range readable(work, part, cutoff) {
		if work.records[key].at >= at {
			low = key.offset
			break
		}
	}
	return answer{rows: [][]driver.Value{{head.next, low}}}
}

// blob reads a byte argument, nil for NULL.
func blob(v driver.Value) []byte {
	b, _ := v.([]byte)
	return slices.Clone(b)
}

// nullable writes a stored byte column back: NULL for none.
func nullable(b []byte) driver.Value {
	if b == nil {
		return nil
	}
	return slices.Clone(b)
}

// fakeSQLRows is a result set.
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
