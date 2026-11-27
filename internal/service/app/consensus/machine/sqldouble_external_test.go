// Package machine_test — the SQL double every test of the SQL machine runs
// against: a database/sql driver over memory that knows the machine's
// position table and one table of the tests' own, entries (idx, data).
//
// A transaction works on a copy of the committed state and replaces it at
// its commit, so a reader sees what was committed when it began: the
// snapshot isolation of a REPEATABLE READ, enough for one writer, which is
// what a state machine is. A statement it does not know fails, so a
// machine that sends one this double does not model fails its test.
package machine_test

import (
	"bufio"
	"context"
	stdsql "database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/service/app/consensus/machine"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"
)

// errCrash is the failure the double injects at the position's write.
var errCrash = errors.New("the process died before the position was written")

// entryRow is one row of the tests' entries table.
type entryRow struct {
	idx  int64
	data string
}

// dbState is the database: the position row (nil when there is none) and
// the entries.
type dbState struct {
	pos     []driver.Value
	entries []entryRow
}

func (s dbState) clone() dbState {
	return dbState{pos: append([]driver.Value(nil), s.pos...), entries: append([]entryRow(nil), s.entries...)}
}

// memDB is one database: what is committed, and the failure armed for the
// position's next write at or past an index.
type memDB struct {
	mu        sync.Mutex
	committed dbState
	// fmu guards the armed failure, read inside a transaction where mu is
	// not held.
	fmu    sync.Mutex
	failAt int64
	failed bool
	// lock is the position row's lock, one slot: a transaction that
	// updates the row holds it until it ends, as a server's row lock.
	lockOnce sync.Once
	lock     chan struct{}
	// inflight, when set, has the next read-write commit that holds the
	// row lock answer an error at once and commit after that delay: a
	// COMMIT the server still runs when the client gives up. committedLate
	// is closed once it committed.
	inflight      time.Duration
	committedLate chan struct{}
}

// rowLock returns the position row's lock.
func (d *memDB) rowLock() chan struct{} {
	d.lockOnce.Do(func() { d.lock = make(chan struct{}, 1) })
	return d.lock
}

// armInflight has the next read-write commit holding the row lock commit
// delay after it answered an error.
func (d *memDB) armInflight(delay time.Duration) {
	d.fmu.Lock()
	d.inflight, d.committedLate = delay, make(chan struct{})
	d.fmu.Unlock()
}

// takeInflight disarms and returns the delay armed, zero when none.
func (d *memDB) takeInflight() (time.Duration, chan struct{}) {
	d.fmu.Lock()
	defer d.fmu.Unlock()
	delay, done := d.inflight, d.committedLate
	d.inflight = 0
	return delay, done
}

// crashNow reports whether a position write at idx fails, and disarms it.
func (d *memDB) crashNow(idx int64) bool {
	d.fmu.Lock()
	defer d.fmu.Unlock()
	if d.failAt > 0 && idx >= d.failAt && !d.failed {
		d.failed = true
		return true
	}
	return false
}

// armCrash makes the next position write at or past index fail, once.
func (d *memDB) armCrash(index int64) {
	d.fmu.Lock()
	d.failAt, d.failed = index, false
	d.fmu.Unlock()
}

// crashed reports whether the armed failure fired.
func (d *memDB) crashed() bool {
	d.fmu.Lock()
	defer d.fmu.Unlock()
	return d.failed
}

// rows returns the committed entries.
func (d *memDB) rows() []entryRow {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]entryRow(nil), d.committed.entries...)
}

func (d *memDB) Connect(context.Context) (driver.Conn, error) { return &memConn{db: d}, nil }
func (d *memDB) Driver() driver.Driver                        { return memDriver{d} }

type memDriver struct{ db *memDB }

func (m memDriver) Open(string) (driver.Conn, error) { return &memConn{db: m.db}, nil }

// memConn is one connection, with the transaction it runs if any, and
// whether that transaction is read-only: a snapshot's, whose commit must
// not write back the copy it read, or it erased every batch committed
// while it was open.
type memConn struct {
	db       *memDB
	tx       *dbState
	readOnly bool
	// locked is set while the transaction holds the position row's lock.
	locked bool
}

// lockRow takes the position row's lock for the transaction, waiting for
// the one that holds it to end or for ctx, then reads the row as committed
// now: a statement that waited on a row lock works on the version the
// holder left, as READ COMMITTED does.
func (c *memConn) lockRow(ctx context.Context) error {
	//: a transaction takes the lock once.
	if c.tx == nil || c.locked {
		return nil
	}
	select {
	//: the lock is free, or its holder ended.
	case c.db.rowLock() <- struct{}{}:
	//: the caller's deadline.
	case <-ctx.Done():
		return ctx.Err()
	}
	c.locked = true
	c.db.mu.Lock()
	c.tx.pos = append([]driver.Value(nil), c.db.committed.pos...)
	c.db.mu.Unlock()
	return nil
}

// unlockRow lets the row's lock go at the transaction's end.
func (c *memConn) unlockRow() {
	//: only a holder lets it go.
	if c.locked {
		c.locked = false
		<-c.db.rowLock()
	}
}

func (c *memConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("the double prepares nothing")
}
func (c *memConn) Close() error { return nil }
func (c *memConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *memConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.db.mu.Lock()
	s := c.db.committed.clone()
	c.db.mu.Unlock()
	c.tx, c.readOnly = &s, opts.ReadOnly
	return memTx{c}, nil
}

type memTx struct{ c *memConn }

func (t memTx) Commit() error {
	//: an armed commit of a row-lock holder answers before it commits.
	if !t.c.readOnly && t.c.locked {
		//: armed.
		if delay, done := t.c.db.takeInflight(); delay > 0 {
			s, d := *t.c.tx, t.c.db
			t.c.tx, t.c.locked = nil, false
			go func() {
				time.Sleep(delay)
				d.mu.Lock()
				d.committed = s
				d.mu.Unlock()
				<-d.rowLock()
				close(done)
			}()
			return errors.New("the connection broke while COMMIT was in flight")
		}
	}
	//: a read-only transaction has nothing to write back.
	if !t.c.readOnly {
		t.c.db.mu.Lock()
		t.c.db.committed = *t.c.tx
		t.c.db.mu.Unlock()
	}
	t.c.tx = nil
	t.c.unlockRow()
	return nil
}

func (t memTx) Rollback() error {
	t.c.tx = nil
	t.c.unlockRow()
	return nil
}

// state runs fn on the transaction's state, or on the committed state in
// a statement of its own.
func (c *memConn) state(fn func(s *dbState) error) error {
	if c.tx != nil {
		return fn(c.tx)
	}
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	s := c.db.committed.clone()
	if err := fn(&s); err != nil {
		return err
	}
	c.db.committed = s
	return nil
}

func values(args []driver.NamedValue) []driver.Value {
	v := make([]driver.Value, len(args))
	for i, a := range args {
		v[i] = a.Value
	}
	return v
}

func (c *memConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	//: a write of the position row takes its lock first.
	if strings.HasPrefix(q, "UPDATE \""+machine.DefaultPositionTable+"\"") {
		//: a deadline met waiting is the statement's error.
		if err := c.lockRow(ctx); err != nil {
			return nil, err
		}
	}
	var n int64
	err := c.state(func(s *dbState) error {
		switch {
		case strings.HasPrefix(q, "UPDATE \""+machine.DefaultPositionTable+"\" SET id = id"):
			//: the row lock alone.
			if s.pos != nil {
				n = 1
			}
		case strings.HasPrefix(q, "CREATE TABLE IF NOT EXISTS"):
		case strings.HasPrefix(q, "INSERT INTO \""+machine.DefaultPositionTable+"\""):
			s.pos, n = values(args), 1
		case strings.HasPrefix(q, "UPDATE \""+machine.DefaultPositionTable+"\""):
			if c.db.crashNow(args[7].Value.(int64)) {
				return errCrash
			}
			if s.pos != nil {
				s.pos, n = values(args), 1
			}
		case strings.HasPrefix(q, "INSERT INTO entries"):
			s.entries, n = append(s.entries, entryRow{idx: args[0].Value.(int64), data: args[1].Value.(string)}), 1
		case q == "DELETE FROM entries":
			n, s.entries = int64(len(s.entries)), nil
		default:
			return fmt.Errorf("the double does not know %q", q)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return driver.RowsAffected(n), nil
}

func (c *memConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	var r *memRows
	err := c.state(func(s *dbState) error {
		switch {
		case strings.HasPrefix(q, "SELECT cluster,"):
			r = &memRows{cols: 8}
			if s.pos != nil {
				r.rows = [][]driver.Value{s.pos}
			}
		case q == "SELECT idx, data FROM entries ORDER BY idx":
			r = &memRows{cols: 2}
			for _, e := range s.entries {
				r.rows = append(r.rows, []driver.Value{e.idx, e.data})
			}
		default:
			return fmt.Errorf("the double does not know %q", q)
		}
		return nil
	})
	return r, err
}

type memRows struct {
	cols int
	rows [][]driver.Value
}

func (r *memRows) Columns() []string { return make([]string, r.cols) }
func (r *memRows) Close() error      { return nil }

func (r *memRows) Next(dest []driver.Value) error {
	if len(r.rows) == 0 {
		return io.EOF
	}
	copy(dest, r.rows[0])
	r.rows = r.rows[1:]
	return nil
}

// newDB returns a database and the SDK's transactor over it.
func newDB(t testing.TB) (*memDB, coresql.Transactor) {
	t.Helper()
	d := &memDB{}
	return d, transactorOver(t, d)
}

// transactorOver opens another pool and transactor on d: a restarted
// process reopening its database.
func transactorOver(t testing.TB, d *memDB) coresql.Transactor {
	t.Helper()
	db := stdsql.OpenDB(d)
	t.Cleanup(func() { _ = db.Close() })
	tm, err := svcsql.NewTransactor(svcsql.Config{DB: db, Dialect: coresql.DialectSQLite, Pool: svcsql.PoolConfig{MaxOpen: 4}})
	if err != nil {
		t.Fatalf("NewTransactor() = %v", err)
	}
	return tm
}

// entriesConfig is an SQL machine that records each command in entries.
func entriesConfig(tm coresql.Transactor) machine.SQLConfig {
	return machine.SQLConfig{
		Transactor: tm,
		Dialect:    coresql.DialectSQLite,
		Apply: func(ctx context.Context, ex coresql.Executor, e coreconsensus.EntryValue) (any, error) {
			_, err := ex.ExecContext(ctx, "INSERT INTO entries (idx, data) VALUES (?, ?)", int64(e.Position.Index), string(e.Data))
			return string(e.Data), err
		},
		Dump: func(ctx context.Context, ex coresql.Executor, w io.Writer) error {
			rows, err := ex.QueryContext(ctx, "SELECT idx, data FROM entries ORDER BY idx")
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var idx int64
				var data string
				if err := rows.Scan(&idx, &data); err != nil {
					return err
				}
				if _, err := fmt.Fprintf(w, "%d %s\n", idx, data); err != nil {
					return err
				}
			}
			return rows.Err()
		},
		Truncate: func(ctx context.Context, ex coresql.Executor) error {
			_, err := ex.ExecContext(ctx, "DELETE FROM entries")
			return err
		},
		Load: func(ctx context.Context, ex coresql.Executor, r io.Reader) error {
			sc := bufio.NewScanner(r)
			for sc.Scan() {
				idx, data, _ := strings.Cut(sc.Text(), " ")
				n, err := strconv.ParseInt(idx, 10, 64)
				if err != nil {
					return err
				}
				if _, err := ex.ExecContext(ctx, "INSERT INTO entries (idx, data) VALUES (?, ?)", n, data); err != nil {
					return err
				}
			}
			return sc.Err()
		},
	}
}
