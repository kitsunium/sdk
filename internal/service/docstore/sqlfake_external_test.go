// Package docstore_test — a SQL engine small enough to read, that understands
// exactly the statements the SQL store sends and nothing else.
//
// # Why an engine and not a live database
//
// The SDK ships no driver and its service module imports none (ADR 0055 §D2),
// so the default suite cannot open PostgreSQL, MySQL or SQLite. database/sql is
// a registry, though: this file registers a driver.Driver whose connections
// keep two tables in maps, and everything above it — the pool, *sql.Tx, the
// transactor's savepoints, ErrTxDone — is the real standard library and the
// real SDK. What it buys: the store's behaviour under the race detector in the
// default lane, a statement log, the two constraints the tables declare,
// PostgreSQL's aborted-transaction state, and failures and concurrent commits
// injected at a named statement — the races a live database will not produce on
// demand.
//
// What it does NOT verify is that an engine accepts the SQL. The statement
// texts are pinned separately, and third-party/db/sql runs the same store on
// the three real engines, behind the integration tag.
//
// Transactions are serialised — one writer from BEGIN to its end, as SQLite
// runs them — and read their own working copy. A concurrent commit is injected
// with commitElsewhere, which is visible to every open transaction, as a READ
// COMMITTED read sees it.
package docstore_test

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

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	"github.com/kitsunium/sdk/internal/service/docstore"
)

// errDuplicate is the fake engine's constraint violation. Its text quotes the
// key the way a real driver's does, so a test can prove the store never lets
// it through.
type errDuplicate struct{ key string }

// Error quotes the key, as PostgreSQL's DETAIL and MySQL's message both do.
func (e errDuplicate) Error() string {
	return "duplicate key value violates a constraint: Key=(" + e.key + ") already exists"
}

// errAborted is PostgreSQL's answer to a statement after a failure in the same
// transaction, until the transaction or a savepoint rolls back.
var errAborted = errors.New("current transaction is aborted, commands ignored until end of transaction block")

// docRow is one row of the documents' table.
type docRow struct {
	doc []byte
	rev int64
}

// ixKey is the index table's primary key.
type ixKey struct{ name, key, doc string }

// vsKey is the versions table's primary key.
type vsKey struct {
	doc string
	num int64
}

// vsRow is one row of the versions table: NULL is nil in every column.
type vsRow struct {
	at   driver.Value // nil or int64
	meta []byte
	doc  []byte
}

// tables is one version of the three tables.
type tables struct {
	docs map[string]docRow
	ix   map[ixKey]bool // the value is uniq: true for a unique index's row
	vs   map[vsKey]vsRow
}

// clone copies t, so a transaction or a savepoint can be undone.
func (t *tables) clone() *tables {
	return &tables{docs: maps.Clone(t.docs), ix: maps.Clone(t.ix), vs: maps.Clone(t.vs)}
}

// versionsOf returns the numbers of key's version rows, newest first, the
// former ones only when formers.
func (t *tables) versionsOf(key string, formers bool) []int64 {
	var nums []int64
	for k, row := range t.vs {
		if k.doc == key && (!formers || row.doc != nil) {
			nums = append(nums, k.num)
		}
	}
	slices.Sort(nums)
	slices.Reverse(nums)
	return nums
}

// uniqueHolder returns the document another unique row files under (name, key).
func (t *tables) uniqueHolder(name, key, except string) (string, bool) {
	for k, uniq := range t.ix {
		if uniq && k.name == name && k.key == key && k.doc != except {
			return k.doc, true
		}
	}
	return "", false
}

// sqlEngine is the fake database.
type sqlEngine struct {
	// writer is held by a transaction from its BEGIN to its end, and by an
	// autocommit write for its one statement.
	writer sync.Mutex
	// mu guards everything below.
	mu sync.Mutex
	// committed is the tables as every new transaction and every autocommit
	// statement sees them.
	committed *tables
	// current is the transaction holding the writer, or nil.
	current *fakeSQLTx
	// log records every statement's role, in order.
	log []string
	// failures fails the next execution of a role with an error.
	failures map[string][]error
	// before runs once, right before the next execution of a role.
	before map[string]func()
	// stmts recognises what the store sends.
	stmts docstore.RenderedSQL
	// dialect decides the aborted state and the lock statement's shape.
	dialect coresql.Dialect
}

// newSQLEngine returns an empty engine for the store keeping its documents in
// table on dialect.
func newSQLEngine(dialect coresql.Dialect, table string) *sqlEngine {
	return &sqlEngine{
		committed: &tables{docs: map[string]docRow{}, ix: map[ixKey]bool{}, vs: map[vsKey]vsRow{}},
		failures:  map[string][]error{},
		before:    map[string]func(){},
		stmts:     docstore.RenderSQLForTest(dialect, table),
		dialect:   dialect,
	}
}

// failNext makes the next execution of role fail with err.
func (e *sqlEngine) failNext(role string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.failures[role] = append(e.failures[role], err)
}

// beforeNext runs fn right before the next execution of role.
func (e *sqlEngine) beforeNext(role string, fn func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.before[role] = fn
}

// commitElsewhere applies change as another transaction committing it: to the
// committed tables, and to the open transaction and every savepoint of it,
// which a READ COMMITTED statement would see. It may run from a beforeNext
// hook, while the store's transaction holds the writer.
func (e *sqlEngine) commitElsewhere(change func(*tables)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	change(e.committed)
	if e.current != nil {
		change(e.current.work)
		for _, sp := range e.current.savepoints {
			change(sp.snapshot)
		}
	}
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

// snapshot returns a copy of the committed tables.
func (e *sqlEngine) snapshot() *tables {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.committed.clone()
}

// fakeSQLSeq numbers registered driver names so tests never collide in
// database/sql's process-wide registry.
var fakeSQLSeq atomic.Int64

// open registers the engine under a fresh driver name and returns a pool.
func (e *sqlEngine) open(t *testing.T) *stdsql.DB {
	t.Helper()
	name := "docstorefake" + strconv.FormatInt(fakeSQLSeq.Add(1), 10)
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
	snapshot *tables
	name     string
}

// fakeSQLTx is one transaction's working copy and savepoints.
type fakeSQLTx struct {
	conn       *fakeSQLConn
	work       *tables
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
func (c *fakeSQLConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
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
	e.current = c.tx
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
	e.current, t.conn.tx = nil, nil
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
	e.current, t.conn.tx = nil, nil
	e.mu.Unlock()
	e.writer.Unlock()
	return nil
}

// answer is what one statement answers: a result set, or a count of the
// rows it affected.
type answer struct {
	cols     []string
	rows     [][]driver.Value
	affected int64
}

// none is the answer of a statement that returns nothing and affected nothing.
var none answer

// rowsOf is an answer holding a result set.
func rowsOf(cols []string, rows [][]driver.Value) answer {
	return answer{cols: cols, rows: rows}
}

// affecting is an answer counting affected rows.
func affecting(n int64) answer {
	return answer{affected: n}
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
		return none, ctxErr
	}
	e := c.engine
	role := e.recognise(query, len(args))
	e.mu.Lock()
	hook := e.before[role]
	delete(e.before, role)
	e.mu.Unlock()
	if hook != nil {
		hook()
	}
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
		return none, queue[0]
	}
	if c.tx == nil {
		work := e.committed.clone()
		ans, err := e.execute(role, query, args, work, work)
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
	case "getDoc", "listDocs", "entries", "entriesLimit", "count", "exists", "lookup", "find",
		"pageAfter", "uniqueTaken", "sharedKeys", "readVersions", "versionHead", "pruneCut", "lockFormers":
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
		return none, nil
	case strings.HasPrefix(query, "ROLLBACK TO SAVEPOINT "):
		at := t.savepointAt(strings.TrimPrefix(query, "ROLLBACK TO SAVEPOINT "))
		if at < 0 {
			return none, errors.New("no such savepoint")
		}
		t.work, t.aborted = t.savepoints[at].snapshot.clone(), false
		t.savepoints = t.savepoints[:at+1]
		return none, nil
	case strings.HasPrefix(query, "RELEASE SAVEPOINT "):
		at := t.savepointAt(strings.TrimPrefix(query, "RELEASE SAVEPOINT "))
		if at < 0 {
			return none, errors.New("no such savepoint")
		}
		t.savepoints = t.savepoints[:at]
		return none, nil
	}
	if t.aborted {
		return none, errAborted
	}
	ans, err := e.execute(role, query, args, t.work, e.committed)
	if err != nil && e.dialect == coresql.DialectPostgres {
		t.aborted = true
	}
	return ans, err
}

// savepointAt finds the savepoint named name, or -1.
func (t *fakeSQLTx) savepointAt(name string) int {
	return slices.IndexFunc(t.savepoints, func(sp savepoint) bool { return sp.name == name })
}

// recognise names the role of a statement the store sent, or returns the
// statement itself for one it did not (a savepoint, or a stranger).
func (e *sqlEngine) recognise(query string, nargs int) string {
	if role, ok := e.stmts.Fixed[query]; ok {
		return role
	}
	switch {
	case nargs%4 == 0 && nargs > 0 && query == e.stmts.InsertIndexRows(nargs/4):
		return "insertIndexRows"
	case nargs%2 == 1 && (query == e.stmts.UniqueTaken(nargs/2, false) || query == e.stmts.UniqueTaken(nargs/2, true)):
		return "uniqueTaken"
	case nargs > 0 && query == e.stmts.SharedKeys(nargs):
		return "sharedKeys"
	case nargs > 0 && query == e.stmts.MarkUnique(nargs):
		return "markUnique"
	case nargs%5 == 0 && nargs > 0 && query == e.stmts.InsertVersions(nargs/5):
		return "insertVersions"
	}
	return query
}

// execute runs one of the store's statements over work, the tables the
// statement reads and writes; latest is what a locking read sees on MySQL.
func (e *sqlEngine) execute(role, query string, args []driver.Value, work, latest *tables) (answer, error) {
	// A read answers rows; anything else is a write.
	if ans, isRead := e.read(role, query, args, work, latest); isRead {
		return ans, nil
	}
	return e.write(role, query, args, work)
}

// read answers the store's reads, and reports false for any other statement.
func (e *sqlEngine) read(role, query string, args []driver.Value, work, latest *tables) (answer, bool) {
	str := func(i int) string { return string(args[i].([]byte)) }
	switch role {
	// One document, or whether one is stored.
	case "getDoc", "exists":
		return e.readOne(role, query, str(0), work, latest), true
	// Every document, in key order.
	case "listDocs":
		return rowsOf([]string{"doc"}, docRows(work, "", 0, false)), true
	// Every document with its key, all of them or the first n.
	case "entries":
		return rowsOf([]string{"doc_key", "doc"}, docRows(work, "", 0, true)), true
	case "entriesLimit":
		return rowsOf([]string{"doc_key", "doc"}, docRows(work, "", int(args[0].(int64)), true)), true
	// Reindex's next page.
	case "pageAfter":
		return rowsOf([]string{"doc_key", "doc"}, docRows(work, str(0), int(args[1].(int64)), true)), true
	// How many documents.
	case "count":
		return rowsOf([]string{"n"}, [][]driver.Value{{int64(len(work.docs))}}), true
	// Which unique indexes file one of the keys elsewhere.
	case "uniqueTaken":
		return e.uniqueTaken(query, args, work, latest), true
	// The documents one index key files; NULL — the empty key — equals none.
	case "lookup", "find":
		if args[1] == nil {
			return rowsOf([]string{"doc"}, nil), true
		}
		return e.lookup(work, str(0), str(1)), true
	// Reindex's check of the unique indexes.
	case "sharedKeys":
		return e.sharedKeys(work, args), true
	// A document's versions, and what a write reads of them.
	case "readVersions":
		return e.readVersions(work, str(0)), true
	case "versionHead":
		for k, row := range work.vs {
			if k.doc == str(0) && row.doc == nil {
				return rowsOf([]string{"num"}, [][]driver.Value{{k.num}}), true
			}
		}
		return rowsOf([]string{"num"}, nil), true
	case "pruneCut":
		formers := work.versionsOf(str(0), true)
		if offset := int(args[1].(int64)); offset < len(formers) {
			return rowsOf([]string{"num"}, [][]driver.Value{{formers[offset]}}), true
		}
		return rowsOf([]string{"num"}, nil), true
	case "lockFormers":
		var rows [][]driver.Value
		for _, num := range work.versionsOf(str(0), true) {
			row := work.vs[vsKey{doc: str(0), num: num}]
			rows = append(rows, []driver.Value{num, row.at, nullable(row.meta), bytes.Clone(row.doc)})
		}
		return rowsOf([]string{"num", "made_at", "meta", "doc"}, rows), true
	}
	return none, false
}

// readVersions answers every version of a document with the document itself,
// newest first, as the LEFT JOIN does: one row without a number for a
// document no version row names, none for no document.
func (e *sqlEngine) readVersions(work *tables, key string) answer {
	cols := []string{"num", "made_at", "meta", "doc"}
	current, found := work.docs[key]
	if !found {
		return rowsOf(cols, nil)
	}
	nums := work.versionsOf(key, false)
	if len(nums) == 0 {
		return rowsOf(cols, [][]driver.Value{{nil, nil, nil, bytes.Clone(current.doc)}})
	}
	rows := make([][]driver.Value, 0, len(nums))
	for _, num := range nums {
		row := work.vs[vsKey{doc: key, num: num}]
		doc := row.doc
		if doc == nil {
			doc = current.doc
		}
		rows = append(rows, []driver.Value{num, row.at, nullable(row.meta), bytes.Clone(doc)})
	}
	return rowsOf(cols, rows)
}

// nullable is b as a column value: nil, which is NULL, when b is nil.
func nullable(b []byte) driver.Value {
	if b == nil {
		return nil
	}
	return bytes.Clone(b)
}

// write runs the store's writes.
func (e *sqlEngine) write(role, query string, args []driver.Value, work *tables) (answer, error) {
	str := func(i int) string { return string(args[i].([]byte)) }
	switch role {
	// The three write modes' statements.
	case "upsert":
		return e.upsert(work, str(0), args[1].([]byte)), nil
	case "insert":
		return e.insert(work, str(0), args[1].([]byte))
	case "replace", "writeLocked":
		return e.replace(role, work, str(1), args[0].([]byte)), nil
	// Update's locking read, a write on SQLite.
	case "lockDoc":
		return e.lock(work, str(0)), nil
	// A document, and its index rows.
	case "deleteDoc":
		if _, found := work.docs[str(0)]; !found {
			return none, nil
		}
		delete(work.docs, str(0))
		return affecting(1), nil
	case "deleteIndex":
		return affecting(dropRows(work, func(k ixKey) bool { return k.doc == str(0) })), nil
	// Reindex's clearing of every row, and its constraining of the unique ones.
	case "clearIndex":
		return affecting(dropRows(work, func(ixKey) bool { return true })), nil
	case "markUnique":
		names := stringArgs(args)
		for k := range work.ix {
			if slices.Contains(names, k.name) {
				work.ix[k] = true
			}
		}
		return none, nil
	// A document's index rows.
	case "insertIndexRows":
		return e.insertRows(work, args)
	// A Put's claim on a store that keeps versions.
	case "claim":
		return e.claim(work, str(0), args[1].([]byte)), nil
	// A document's versions.
	case "retireHead":
		k := vsKey{doc: str(1), num: args[2].(int64)}
		row, found := work.vs[k]
		if !found {
			return none, nil
		}
		row.doc = bytes.Clone(args[0].([]byte))
		work.vs[k] = row
		return affecting(1), nil
	case "insertVersions":
		return e.insertVersions(work, args)
	case "pruneVersions":
		cut := args[1].(int64)
		return affecting(dropVersions(work, str(0), func(num int64, row vsRow) bool { return row.doc != nil && num <= cut })), nil
	case "dropVersions":
		return affecting(dropVersions(work, str(0), func(int64, vsRow) bool { return true })), nil
	case "dropFormers":
		return affecting(dropVersions(work, str(0), func(_ int64, row vsRow) bool { return row.doc != nil })), nil
	}
	return none, errors.New("the fake engine does not speak: " + query)
}

// claim is a Put's first statement on a store that keeps versions: it creates
// the document, or moves the stored one's revision and leaves it as it is,
// answering the way the dialect does.
func (e *sqlEngine) claim(work *tables, key string, doc []byte) answer {
	row, found := work.docs[key]
	if found {
		row.rev++
	} else {
		row = docRow{doc: bytes.Clone(doc), rev: 1}
	}
	work.docs[key] = row
	if e.dialect != coresql.DialectMySQL {
		return rowsOf([]string{"rev", "doc"}, [][]driver.Value{{row.rev, bytes.Clone(row.doc)}})
	}
	if found {
		return affecting(2)
	}
	return affecting(1)
}

// insertVersions inserts version rows, refusing one whose key and number are
// taken, as the primary key does; a statement that fails leaves nothing of
// itself behind.
func (e *sqlEngine) insertVersions(work *tables, args []driver.Value) (answer, error) {
	var added []vsKey
	for i := 0; i < len(args); i += 5 {
		k := vsKey{doc: string(args[i].([]byte)), num: args[i+1].(int64)}
		if _, taken := work.vs[k]; taken {
			for _, undo := range added {
				delete(work.vs, undo)
			}
			return none, errDuplicate{key: k.doc}
		}
		row := vsRow{at: args[i+2]}
		if meta, ok := args[i+3].([]byte); ok {
			row.meta = bytes.Clone(meta)
		}
		if doc, ok := args[i+4].([]byte); ok {
			row.doc = bytes.Clone(doc)
		}
		work.vs[k] = row
		added = append(added, k)
	}
	return affecting(int64(len(added))), nil
}

// dropVersions deletes the version rows of key gone accepts and counts them.
func dropVersions(work *tables, key string, gone func(num int64, row vsRow) bool) int64 {
	n := int64(0)
	for k, row := range work.vs {
		if k.doc == key && gone(k.num, row) {
			delete(work.vs, k)
			n++
		}
	}
	return n
}

// readOne answers getDoc — the document — or exists — a 1 — for key, reading
// past the transaction's copy for MySQL's shared lock.
func (e *sqlEngine) readOne(role, query, key string, work, latest *tables) answer {
	row, found := work.docs[key]
	if !found && strings.HasSuffix(query, "LOCK IN SHARE MODE") {
		row, found = latest.docs[key]
	}
	switch {
	case !found:
		return rowsOf([]string{"doc"}, nil)
	case role == "exists":
		return rowsOf([]string{"one"}, [][]driver.Value{{int64(1)}})
	default:
		return rowsOf([]string{"doc"}, [][]driver.Value{{bytes.Clone(row.doc)}})
	}
}

// upsert creates or replaces a document, answering the way the dialect does.
func (e *sqlEngine) upsert(work *tables, key string, doc []byte) answer {
	row, found := work.docs[key]
	row = docRow{doc: bytes.Clone(doc), rev: row.rev + 1}
	work.docs[key] = row
	if e.dialect != coresql.DialectMySQL {
		return rowsOf([]string{"rev"}, [][]driver.Value{{row.rev}})
	}
	if found {
		return affecting(2)
	}
	return affecting(1)
}

// insert creates a document, or answers a taken key: an error on MySQL, no
// row affected where ON CONFLICT DO NOTHING exists.
func (e *sqlEngine) insert(work *tables, key string, doc []byte) (answer, error) {
	if _, taken := work.docs[key]; taken {
		if e.dialect == coresql.DialectMySQL {
			return none, errDuplicate{key: key}
		}
		return none, nil
	}
	work.docs[key] = docRow{doc: bytes.Clone(doc), rev: 1}
	return affecting(1), nil
}

// replace rewrites a stored document; SQLite's write under Update's lock
// leaves the revision the locking read already moved.
func (e *sqlEngine) replace(role string, work *tables, key string, doc []byte) answer {
	row, found := work.docs[key]
	if !found {
		return none
	}
	if role == "replace" || e.dialect != coresql.DialectSQLite {
		row.rev++
	}
	row.doc = bytes.Clone(doc)
	work.docs[key] = row
	return affecting(1)
}

// lock is Update's locking read; on SQLite it is a write that moves the
// revision, as the statement it stands for.
func (e *sqlEngine) lock(work *tables, key string) answer {
	row, found := work.docs[key]
	if !found {
		return rowsOf([]string{"doc"}, nil)
	}
	if e.dialect == coresql.DialectSQLite {
		row.rev++
		work.docs[key] = row
	}
	return rowsOf([]string{"doc"}, [][]driver.Value{{bytes.Clone(row.doc)}})
}

// dropRows deletes the index rows gone accepts and counts them.
func dropRows(work *tables, gone func(ixKey) bool) int64 {
	n := int64(0)
	for k := range work.ix {
		if gone(k) {
			delete(work.ix, k)
			n++
		}
	}
	return n
}

// insertRows files index rows, enforcing both constraints of the table; a
// statement that fails leaves nothing of itself behind.
func (e *sqlEngine) insertRows(work *tables, args []driver.Value) (answer, error) {
	var added []ixKey
	for i := 0; i < len(args); i += 4 {
		k := ixKey{name: string(args[i].([]byte)), key: string(args[i+1].([]byte)), doc: string(args[i+2].([]byte))}
		uniq := args[i+3] != nil
		_, taken := work.ix[k]
		_, held := work.uniqueHolder(k.name, k.key, "")
		if taken || (held && uniq) {
			for _, undo := range added {
				delete(work.ix, undo)
			}
			return none, errDuplicate{key: k.key}
		}
		work.ix[k] = uniq
		added = append(added, k)
	}
	return affecting(int64(len(added))), nil
}

// uniqueTaken answers which unique indexes file one of the keys elsewhere.
func (e *sqlEngine) uniqueTaken(query string, args []driver.Value, work, latest *tables) answer {
	source := work
	if strings.HasSuffix(query, "LOCK IN SHARE MODE") {
		source = latest
	}
	owner := string(args[0].([]byte))
	var names [][]driver.Value
	for i := 1; i < len(args); i += 2 {
		name, key := string(args[i].([]byte)), string(args[i+1].([]byte))
		if _, held := source.uniqueHolder(name, key, owner); held {
			names = append(names, []driver.Value{[]byte(name)})
		}
	}
	return rowsOf([]string{"index_name"}, names)
}

// lookup answers the documents one index key files, in key order.
func (e *sqlEngine) lookup(work *tables, name, key string) answer {
	var holders []string
	for k := range work.ix {
		if k.name == name && k.key == key {
			holders = append(holders, k.doc)
		}
	}
	slices.Sort(holders)
	var rows [][]driver.Value
	for _, holder := range holders {
		if row, found := work.docs[holder]; found {
			rows = append(rows, []driver.Value{bytes.Clone(row.doc)})
		}
	}
	return rowsOf([]string{"doc"}, rows)
}

// sharedKeys answers the named indexes that file one key under two documents.
func (e *sqlEngine) sharedKeys(work *tables, args []driver.Value) answer {
	names := stringArgs(args)
	holders := map[[2]string]int{}
	for k := range work.ix {
		if slices.Contains(names, k.name) {
			holders[[2]string{k.name, k.key}]++
		}
	}
	var rows [][]driver.Value
	for group, n := range holders {
		if n > 1 {
			rows = append(rows, []driver.Value{[]byte(group[0])})
		}
	}
	return rowsOf([]string{"index_name"}, rows)
}

// stringArgs reads byte-slice arguments as strings.
func stringArgs(args []driver.Value) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = string(a.([]byte))
	}
	return out
}

// docRows answers documents in key order, above after, at most limit (all
// when limit is 0), with their keys when withKeys.
func docRows(work *tables, after string, limit int, withKeys bool) [][]driver.Value {
	keys := slices.SortedFunc(maps.Keys(work.docs), func(a, b string) int { return cmp.Compare(a, b) })
	var rows [][]driver.Value
	for _, k := range keys {
		if after != "" && k <= after {
			continue
		}
		if limit > 0 && len(rows) == limit {
			break
		}
		doc := bytes.Clone(work.docs[k].doc)
		if withKeys {
			rows = append(rows, []driver.Value{[]byte(k), doc})
		} else {
			rows = append(rows, []driver.Value{doc})
		}
	}
	return rows
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
