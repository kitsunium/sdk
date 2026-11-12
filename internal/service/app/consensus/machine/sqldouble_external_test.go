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

// memConn is one connection, with the transaction it runs if any.
type memConn struct {
	db *memDB
	tx *dbState
}

func (c *memConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("the double prepares nothing")
}
func (c *memConn) Close() error { return nil }
func (c *memConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *memConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.db.mu.Lock()
	s := c.db.committed.clone()
	c.db.mu.Unlock()
	c.tx = &s
	return memTx{c}, nil
}

type memTx struct{ c *memConn }

func (t memTx) Commit() error {
	t.c.db.mu.Lock()
	t.c.db.committed = *t.c.tx
	t.c.db.mu.Unlock()
	t.c.tx = nil
	return nil
}

func (t memTx) Rollback() error { t.c.tx = nil; return nil }

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

func (c *memConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	var n int64
	err := c.state(func(s *dbState) error {
		switch {
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
