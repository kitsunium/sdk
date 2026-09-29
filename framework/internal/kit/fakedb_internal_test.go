package kit

import (
	"context"
	stdsql "database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/kitsunium/sdk/pkg/v1/secret"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// FakeDB is a database for kit's tests, with no network and no driver: the
// statements the SDK's checker and migrator send on the postgres dialect —
// the version table, the advisory lock, a ping —, and the product's
// migrations recorded as statements. It is exported for the external tests
// (package kit_test), in a test file only.
//
// Its engine reads URLs fake://user:password@host:port/database?tls=MODE
// (tls=off turns TLS off; no tls leaves it to the driver) — or, on the
// SQLite dialect, a file's path. Its failures quote the URL on purpose:
// kit must never show one.
type FakeDB struct {
	dialect sql.Dialect

	mu sync.Mutex
	// down refuses every connection and every ping.
	down bool
	// password, when set, is the only one a connection is let in with.
	password string
	// generation retires every connection opened before it moved.
	generation int
	tables     map[string]map[int64]string
	locks      map[int64]*fakeConn
	// executed are the product's statements, in the order they committed.
	executed []string
	// logins are the passwords of the connections opened, in order; urls
	// the URLs they were opened with.
	logins []string
	urls   []string
}

// NewFakeDB is an empty fake database speaking dialect.
func NewFakeDB(dialect sql.Dialect) *FakeDB {
	return &FakeDB{dialect: dialect, tables: map[string]map[int64]string{}, locks: map[int64]*fakeConn{}}
}

// Engine is the fake database's engine.
//
// IFACE-PLUGIN: kit.Database takes the engine through its port; the fake is
// one of its implementations.
func (f *FakeDB) Engine() Engine { return fakeEngine{f} }

// SetDown makes the database refuse or answer again.
func (f *FakeDB) SetDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

// SetPassword makes password the only one the database lets in.
func (f *FakeDB) SetPassword(password string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.password = password
}

// Retire makes every open connection invalid: the pool opens new ones.
func (f *FakeDB) Retire() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.generation++
}

// Executed are the product's statements the database committed.
func (f *FakeDB) Executed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.executed)
}

// Versions are the versions a version table records.
func (f *FakeDB) Versions(table string) []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Sorted(maps.Keys(f.tables[table]))
}

// Logins are the passwords the connections were opened with, in order.
func (f *FakeDB) Logins() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.logins)
}

// URLs are the URLs the connections were opened with, in order.
func (f *FakeDB) URLs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.urls)
}

type fakeEngine struct{ f *FakeDB }

func (e fakeEngine) Dialect() sql.Dialect { return e.f.dialect }

func (e fakeEngine) Describe(v secret.Value) (DatabaseURLValue, error) {
	raw := v.RevealString()
	if e.f.dialect == sql.DialectSQLite {
		return DatabaseURLValue{Driver: "fake", Database: raw}, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "fake" || u.Host == "" {
		return DatabaseURLValue{}, fmt.Errorf("fake: cannot read the URL %s", raw)
	}
	d := DatabaseURLValue{Driver: "fake", Address: u.Host, Database: strings.TrimPrefix(u.Path, "/"), Networked: true}
	switch mode := u.Query().Get("tls"); mode {
	case "off":
		d.TLS, d.Plaintext = "off", true
	default:
		d.TLS = mode
	}
	return d, nil
}

func (e fakeEngine) Open(v secret.Value, current func(context.Context) (secret.Value, error)) (*stdsql.DB, error) {
	if _, err := e.Describe(v); err != nil {
		return nil, err
	}
	return stdsql.OpenDB(&fakeConnector{f: e.f, current: current}), nil
}

type fakeConnector struct {
	f       *FakeDB
	current func(context.Context) (secret.Value, error)
}

func (c *fakeConnector) Connect(ctx context.Context) (driver.Conn, error) {
	v, err := c.current(ctx)
	if err != nil {
		return nil, err
	}
	raw := v.RevealString()
	user, pass := "", ""
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	if c.f.down {
		return nil, fmt.Errorf("fake: dial %s: connection refused", raw)
	}
	if c.f.password != "" && pass != c.f.password {
		return nil, fmt.Errorf("fake: password authentication failed for user %q (%s)", user, raw)
	}
	c.f.logins = append(c.f.logins, pass)
	c.f.urls = append(c.f.urls, raw)
	return &fakeConn{f: c.f, generation: c.f.generation}, nil
}

func (c *fakeConnector) Driver() driver.Driver { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fake: open through the connector")
}

type fakeConn struct {
	f          *FakeDB
	generation int
	tx         *fakeTx
}

// fakeTx buffers what a transaction writes until it commits.
type fakeTx struct {
	c      *fakeConn
	writes []func()
}

func (t *fakeTx) Commit() error {
	t.c.f.mu.Lock()
	for _, w := range t.writes {
		w()
	}
	t.c.f.mu.Unlock()
	t.c.tx = nil
	return nil
}

func (t *fakeTx) Rollback() error {
	t.c.tx = nil
	return nil
}

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake: no prepared statements")
}

func (c *fakeConn) Close() error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	for k, holder := range c.f.locks {
		if holder == c {
			delete(c.f.locks, k)
		}
	}
	return nil
}

func (c *fakeConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *fakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.tx = &fakeTx{c: c}
	return c.tx, nil
}

func (c *fakeConn) Ping(context.Context) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	switch {
	case c.generation != c.f.generation:
		// A retired connection: database/sql closes it and opens another.
		return driver.ErrBadConn
	case c.f.down:
		return errors.New("fake: the server at fake://… went away")
	}
	return nil
}

// ResetSession retires a connection opened before the database's
// generation moved, when the pool hands it out again.
func (c *fakeConn) ResetSession(context.Context) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	if c.generation != c.f.generation {
		return driver.ErrBadConn
	}
	return nil
}

// IsValid retires a connection opened before the database's generation
// moved: database/sql opens a new one.
func (c *fakeConn) IsValid() bool {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	return c.generation == c.f.generation && !c.f.down
}

// write applies fn now, or when the transaction commits.
func (c *fakeConn) write(fn func()) {
	if c.tx != nil {
		c.tx.writes = append(c.tx.writes, fn)
		return
	}
	c.f.mu.Lock()
	fn()
	c.f.mu.Unlock()
}

func (c *fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	f := c.f
	switch {
	case strings.HasPrefix(query, "CREATE TABLE IF NOT EXISTS "):
		table := strings.Fields(query)[5]
		f.mu.Lock()
		if f.tables[table] == nil {
			f.tables[table] = map[int64]string{}
		}
		f.mu.Unlock()
	case strings.HasPrefix(query, "INSERT INTO "):
		table := strings.Fields(query)[2]
		version, name := args[0].Value.(int64), args[1].Value.(string)
		c.write(func() { f.tables[table][version] = name })
	case strings.HasPrefix(query, "DELETE FROM "):
		table := strings.Fields(query)[2]
		version := args[0].Value.(int64)
		c.write(func() { delete(f.tables[table], version) })
	case strings.HasPrefix(query, "SELECT pg_advisory_unlock("):
		key := args[0].Value.(int64)
		f.mu.Lock()
		if f.locks[key] == c {
			delete(f.locks, key)
		}
		f.mu.Unlock()
	case strings.Contains(query, "FAIL"):
		return nil, fmt.Errorf("fake: syntax error in %q", query)
	default:
		c.write(func() { f.executed = append(f.executed, query) })
	}
	return driver.RowsAffected(1), nil
}

func (c *fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	f := c.f
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasPrefix(query, "SELECT pg_try_advisory_lock("):
		key := args[0].Value.(int64)
		if holder, held := f.locks[key]; held && holder != c {
			return &fakeRows{cols: []string{"granted"}, rows: [][]driver.Value{{false}}}, nil
		}
		f.locks[key] = c
		return &fakeRows{cols: []string{"granted"}, rows: [][]driver.Value{{true}}}, nil
	case strings.HasPrefix(query, "SELECT pg_advisory_unlock("):
		key := args[0].Value.(int64)
		if f.locks[key] == c {
			delete(f.locks, key)
		}
		return &fakeRows{cols: []string{"released"}, rows: [][]driver.Value{{true}}}, nil
	case strings.HasPrefix(query, "SELECT version FROM "):
		table := strings.Fields(query)[3]
		rows, ok := f.tables[table]
		if !ok {
			return nil, fmt.Errorf("fake: no table %s", table)
		}
		out := &fakeRows{cols: []string{"version"}}
		for _, v := range slices.Sorted(maps.Keys(rows)) {
			out.rows = append(out.rows, []driver.Value{v})
		}
		return out, nil
	}
	return nil, fmt.Errorf("fake: cannot answer %q", query)
}

type fakeRows struct {
	cols []string
	rows [][]driver.Value
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if len(r.rows) == 0 {
		return io.EOF
	}
	copy(dest, r.rows[0])
	r.rows = r.rows[1:]
	return nil
}

// MigrateCommand runs the app's migrate command on the given streams, for
// the external tests.
func (a *App) MigrateCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return a.migrateCommand(ctx, args, stdout, stderr)
}
