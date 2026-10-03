package kit

import (
	"bytes"
	"context"
	stdsql "database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// FakeDB is a database for kit's tests, with no network and no driver: the
// statements the SDK's checker and migrator send — the version table, the
// advisory lock, a ping —, the product's migrations recorded as statements,
// and the tables of the SDK's document store over SQL, which kit's stores run
// on. It is exported for the external tests (package kit_test), in a test
// file only.
//
// Transactions are serialised, one from its BEGIN to its end, as SQLite runs
// them, each on a copy of the data it commits or drops; a savepoint is a copy
// of that copy. A statement outside a transaction waits for the one that
// runs, then applies at once.
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
	data       *fakeData
	locks      map[int64]*fakeConn
	// logins are the passwords of the connections opened, in order; urls
	// the URLs they were opened with.
	logins []string
	urls   []string
	// turn is held by the transaction that runs: one at a time.
	turn chan struct{}
}

// fakeData is what the database holds: its version tables, the product's
// statements, the document tables, and the tables of the documents'
// versions (ADR 0143), by table, key and number.
type fakeData struct {
	versions map[string]map[int64]string
	// executed are the product's statements, in the order they committed.
	executed []string
	docs     map[string]*fakeTable
	vs       map[string]map[string]map[int64]fakeVersion
}

// fakeVersion is one row of a versions table: nil where the column is NULL
// — doc for the current version, whose document is the documents' table's.
type fakeVersion struct {
	at, ns    any
	meta, doc []byte
}

// fakeTable is one document table and its index rows.
type fakeTable struct {
	rows map[string]fakeRow
	// ix are the index rows, their value whether the row is a unique
	// index's.
	ix map[fakeIx]bool
}

type fakeRow struct {
	doc []byte
	rev int64
}

type fakeIx struct{ name, key, doc string }

// clone copies d, so a transaction or a savepoint can be dropped.
func (d *fakeData) clone() *fakeData {
	out := &fakeData{
		versions: map[string]map[int64]string{}, executed: slices.Clone(d.executed), docs: map[string]*fakeTable{},
		vs: map[string]map[string]map[int64]fakeVersion{},
	}
	for t, v := range d.versions {
		out.versions[t] = maps.Clone(v)
	}
	for t, tab := range d.docs {
		out.docs[t] = &fakeTable{rows: maps.Clone(tab.rows), ix: maps.Clone(tab.ix)}
	}
	for t, keys := range d.vs {
		out.vs[t] = map[string]map[int64]fakeVersion{}
		for k, rows := range keys {
			out.vs[t][k] = maps.Clone(rows)
		}
	}
	return out
}

// NewFakeDB is an empty fake database speaking dialect.
func NewFakeDB(dialect sql.Dialect) *FakeDB {
	return &FakeDB{
		dialect: dialect, locks: map[int64]*fakeConn{}, turn: make(chan struct{}, 1),
		data: &fakeData{versions: map[string]map[int64]string{}, docs: map[string]*fakeTable{}, vs: map[string]map[string]map[int64]fakeVersion{}},
	}
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
	return slices.Clone(f.data.executed)
}

// Versions are the versions a version table records.
func (f *FakeDB) Versions(table string) []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Sorted(maps.Keys(f.data.versions[table]))
}

// Tables are the document tables the database holds, sorted.
func (f *FakeDB) Tables() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Sorted(maps.Keys(f.data.docs))
}

// Documents are the documents a table holds, by key, as the store wrote
// them.
func (f *FakeDB) Documents(table string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	if t := f.data.docs[table]; t != nil {
		for k, r := range t.rows {
			out[k] = string(r.doc)
		}
	}
	return out
}

// IndexKeys are the index keys a table's index rows hold, as they reach the
// table.
func (f *FakeDB) IndexKeys(table string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	if t := f.data.docs[table]; t != nil {
		for ix := range t.ix {
			out = append(out, ix.key)
		}
	}
	slices.Sort(out)
	return out
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

// fakeTx is a transaction: the copy of the data it works on, and a copy per
// savepoint, by name.
type fakeTx struct {
	c     *fakeConn
	work  *fakeData
	saves map[string]*fakeData
}

func (t *fakeTx) Commit() error {
	t.c.f.mu.Lock()
	t.c.f.data = t.work
	t.c.f.mu.Unlock()
	t.c.tx = nil
	<-t.c.f.turn
	return nil
}

func (t *fakeTx) Rollback() error {
	t.c.tx = nil
	<-t.c.f.turn
	return nil
}

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake: no prepared statements")
}

func (c *fakeConn) Close() error {
	c.f.mu.Lock()
	for k, holder := range c.f.locks {
		if holder == c {
			delete(c.f.locks, k)
		}
	}
	c.f.mu.Unlock()
	if c.tx != nil {
		c.tx = nil
		<-c.f.turn
	}
	return nil
}

func (c *fakeConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *fakeConn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	select {
	case c.f.turn <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	c.f.mu.Lock()
	c.tx = &fakeTx{c: c, work: c.f.data.clone(), saves: map[string]*fakeData{}}
	c.f.mu.Unlock()
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

// run runs a statement on the data the connection sees: its transaction's
// copy, or the database's own — what is committed, for a read; for a
// write, once the transaction that runs has ended.
func (c *fakeConn) run(ctx context.Context, query string, fn func(d *fakeData) (driver.Rows, int64, error)) (driver.Rows, int64, error) {
	if c.tx != nil {
		return fn(c.tx.work)
	}
	if strings.HasPrefix(query, "SELECT ") && !strings.HasSuffix(query, "FOR UPDATE") {
		c.f.mu.Lock()
		defer c.f.mu.Unlock()
		return fn(c.f.data)
	}
	select {
	case c.f.turn <- struct{}{}:
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
	defer func() { <-c.f.turn }()
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	return fn(c.f.data)
}

func (c *fakeConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if res, handled := c.control(query, args); handled {
		return res, nil
	}
	_, n, err := c.run(ctx, query, func(d *fakeData) (driver.Rows, int64, error) { return statement(d, query, args) })
	if err != nil {
		return nil, err
	}
	return driver.RowsAffected(n), nil
}

func (c *fakeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if rows, handled := c.lockQuery(query, args); handled {
		return rows, nil
	}
	rows, _, err := c.run(ctx, query, func(d *fakeData) (driver.Rows, int64, error) { return statement(d, query, args) })
	if err == nil && rows == nil {
		rows = &fakeRows{}
	}
	return rows, err
}

// control answers what is no data's: a savepoint, the advisory lock's
// release.
func (c *fakeConn) control(query string, args []driver.NamedValue) (driver.Result, bool) {
	switch {
	case strings.HasPrefix(query, "SAVEPOINT "):
		if c.tx != nil {
			c.tx.saves[strings.TrimPrefix(query, "SAVEPOINT ")] = c.tx.work.clone()
		}
		return driver.RowsAffected(0), true
	case strings.HasPrefix(query, "ROLLBACK TO SAVEPOINT "):
		if c.tx != nil {
			if saved := c.tx.saves[strings.TrimPrefix(query, "ROLLBACK TO SAVEPOINT ")]; saved != nil {
				c.tx.work = saved.clone()
			}
		}
		return driver.RowsAffected(0), true
	case strings.HasPrefix(query, "RELEASE SAVEPOINT "):
		if c.tx != nil {
			delete(c.tx.saves, strings.TrimPrefix(query, "RELEASE SAVEPOINT "))
		}
		return driver.RowsAffected(0), true
	case strings.HasPrefix(query, "SELECT pg_advisory_unlock("):
		key := args[0].Value.(int64)
		c.f.mu.Lock()
		if c.f.locks[key] == c {
			delete(c.f.locks, key)
		}
		c.f.mu.Unlock()
		return driver.RowsAffected(1), true
	}
	return nil, false
}

// lockQuery answers the advisory lock's questions.
func (c *fakeConn) lockQuery(query string, args []driver.NamedValue) (driver.Rows, bool) {
	f := c.f
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasPrefix(query, "SELECT pg_try_advisory_lock("):
		key := args[0].Value.(int64)
		if holder, held := f.locks[key]; held && holder != c {
			return &fakeRows{cols: []string{"granted"}, rows: [][]driver.Value{{false}}}, true
		}
		f.locks[key] = c
		return &fakeRows{cols: []string{"granted"}, rows: [][]driver.Value{{true}}}, true
	case strings.HasPrefix(query, "SELECT pg_advisory_unlock("):
		key := args[0].Value.(int64)
		if f.locks[key] == c {
			delete(f.locks, key)
		}
		return &fakeRows{cols: []string{"released"}, rows: [][]driver.Value{{true}}}, true
	}
	return nil, false
}

// quoted finds a quoted table name: the document store quotes its own.
var quoted = regexp.MustCompile(`"([a-z0-9_]+)"`)

// statement runs one statement on d: a version table's, a document
// table's, or the product's own, recorded.
func statement(d *fakeData, query string, args []driver.NamedValue) (driver.Rows, int64, error) {
	if strings.Contains(query, "FAIL") {
		return nil, 0, fmt.Errorf("fake: syntax error in %q", query)
	}
	if strings.Contains(query, "LEFT JOIN") {
		return readVersions(d, query, args)
	}
	if names := quoted.FindStringSubmatch(query); names != nil && strings.HasSuffix(names[1], "___vs") {
		return versionStatement(d, query, strings.TrimSuffix(names[1], "___vs"), args)
	}
	if names := quoted.FindStringSubmatch(query); names != nil {
		return docStatement(d, query, strings.TrimSuffix(names[1], "___ix"), args)
	}
	fields := strings.Fields(query)
	switch {
	case strings.HasPrefix(query, "CREATE TABLE IF NOT EXISTS "):
		if d.versions[fields[5]] == nil {
			d.versions[fields[5]] = map[int64]string{}
		}
		return nil, 0, nil
	case strings.HasPrefix(query, "SELECT version FROM "):
		rows, ok := d.versions[fields[3]]
		if !ok {
			return nil, 0, fmt.Errorf("fake: no table %s", fields[3])
		}
		out := &fakeRows{cols: []string{"version"}}
		for _, v := range slices.Sorted(maps.Keys(rows)) {
			out.rows = append(out.rows, []driver.Value{v})
		}
		return out, 0, nil
	case strings.HasPrefix(query, "INSERT INTO ") && len(args) >= 2:
		version, name := args[0].Value.(int64), args[1].Value.(string)
		d.versions[fields[2]][version] = name
		return nil, 1, nil
	case strings.HasPrefix(query, "DELETE FROM ") && strings.HasSuffix(query, "WHERE 1 = 0"):
		return nil, 0, nil
	case strings.HasPrefix(query, "DELETE FROM ") && len(args) == 1:
		delete(d.versions[fields[2]], args[0].Value.(int64))
		return nil, 1, nil
	}
	d.executed = append(d.executed, query)
	return nil, 1, nil
}

// bound is a bound value as bytes: the document store binds keys and
// documents as []byte.
func bound(v driver.Value) []byte {
	switch b := v.(type) {
	case []byte:
		return b
	case string:
		return []byte(b)
	}
	return nil
}

// boundKey is a bound key as the fake's tables index it.
func boundKey(v driver.Value) string { return string(bound(v)) }

// docStatement runs one of the document store's statements on the table
// name — its index table when it names that one.
func docStatement(d *fakeData, query, name string, args []driver.NamedValue) (driver.Rows, int64, error) {
	arg := func(i int) []byte { return bound(args[i].Value) }
	switch {
	case strings.HasPrefix(query, "CREATE TABLE IF NOT EXISTS "):
		if d.docs[name] == nil {
			d.docs[name] = &fakeTable{rows: map[string]fakeRow{}, ix: map[fakeIx]bool{}}
		}
		return nil, 0, nil
	case strings.HasPrefix(query, "DROP TABLE IF EXISTS "):
		delete(d.docs, name)
		return nil, 0, nil
	}
	t := d.docs[name]
	if t == nil {
		return nil, 0, fmt.Errorf("fake: no table %s", name)
	}
	if strings.Contains(query, "___ix") && !strings.HasPrefix(query, "SELECT d.doc") {
		return indexStatement(t, query, args, arg)
	}
	return docRowStatement(t, query, args, arg)
}

// docRowStatement runs a statement on a table's documents.
func docRowStatement(t *fakeTable, query string, args []driver.NamedValue, arg func(int) []byte) (driver.Rows, int64, error) {
	one := func(col string, v driver.Value) driver.Rows {
		return &fakeRows{cols: []string{col}, rows: [][]driver.Value{{v}}}
	}
	switch {
	case strings.HasPrefix(query, "SELECT doc FROM ") && strings.Contains(query, "WHERE doc_key ="):
		if r, ok := t.rows[string(arg(0))]; ok {
			return one("doc", slices.Clone(r.doc)), 0, nil
		}
		return &fakeRows{cols: []string{"doc"}}, 0, nil
	case strings.HasPrefix(query, "SELECT doc FROM "):
		return t.docs(slices.Sorted(maps.Keys(t.rows))), 0, nil
	case strings.HasPrefix(query, "SELECT doc_key, doc FROM "):
		keys := slices.Sorted(maps.Keys(t.rows))
		limit := len(keys)
		switch {
		case strings.Contains(query, "doc_key >"):
			after := string(arg(0))
			keys = slices.DeleteFunc(keys, func(k string) bool { return k <= after })
			limit = int(args[1].Value.(int64))
		case strings.Contains(query, "LIMIT"):
			limit = int(args[0].Value.(int64))
		}
		out := &fakeRows{cols: []string{"doc_key", "doc"}}
		for _, k := range keys[:min(limit, len(keys))] {
			out.rows = append(out.rows, []driver.Value{[]byte(k), slices.Clone(t.rows[k].doc)})
		}
		return out, 0, nil
	case strings.HasPrefix(query, "SELECT COUNT(*) FROM "):
		return one("count", int64(len(t.rows))), 0, nil
	case strings.HasPrefix(query, "SELECT 1 FROM "):
		if _, ok := t.rows[string(arg(0))]; ok {
			return one("one", int64(1)), 0, nil
		}
		return &fakeRows{cols: []string{"one"}}, 0, nil
	case strings.HasPrefix(query, "SELECT d.doc FROM "):
		var keys []string
		for ix := range t.ix {
			if ix.name == string(arg(0)) && ix.key == string(arg(1)) {
				keys = append(keys, ix.doc)
			}
		}
		slices.Sort(keys)
		return t.docs(keys), 0, nil
	case strings.HasPrefix(query, "INSERT INTO ") && strings.Contains(query, "RETURNING rev, doc"):
		// A claim (ADR 0143): the row created, or locked and left as it is,
		// its revision moved.
		key, doc := string(arg(0)), slices.Clone(arg(1))
		r, taken := t.rows[key]
		if !taken {
			t.rows[key] = fakeRow{doc: doc, rev: 1}
			return &fakeRows{cols: []string{"rev", "doc"}, rows: [][]driver.Value{{int64(1), doc}}}, 1, nil
		}
		t.rows[key] = fakeRow{doc: r.doc, rev: r.rev + 1}
		return &fakeRows{cols: []string{"rev", "doc"}, rows: [][]driver.Value{{r.rev + 1, slices.Clone(r.doc)}}}, 2, nil
	case strings.HasPrefix(query, "INSERT INTO "):
		key, doc := string(arg(0)), slices.Clone(arg(1))
		r, taken := t.rows[key]
		switch {
		case strings.Contains(query, "DO NOTHING") && taken:
			return nil, 0, nil
		case strings.Contains(query, "DO UPDATE") && taken:
			t.rows[key] = fakeRow{doc: doc, rev: r.rev + 1}
		case taken:
			return nil, 0, errors.New("fake: duplicate key in the documents' table")
		default:
			t.rows[key] = fakeRow{doc: doc, rev: 1}
		}
		return one("rev", t.rows[key].rev), 1, nil
	case strings.HasPrefix(query, "UPDATE ") && strings.Contains(query, "RETURNING doc"):
		key := string(arg(0))
		r, ok := t.rows[key]
		if !ok {
			return &fakeRows{cols: []string{"doc"}}, 0, nil
		}
		t.rows[key] = fakeRow{doc: r.doc, rev: r.rev + 1}
		return one("doc", slices.Clone(r.doc)), 1, nil
	case strings.HasPrefix(query, "UPDATE "):
		// The replacement, or Update's write under its lock: the document,
		// then the key.
		key := string(arg(1))
		r, ok := t.rows[key]
		if !ok {
			return nil, 0, nil
		}
		t.rows[key] = fakeRow{doc: slices.Clone(arg(0)), rev: r.rev + 1}
		return nil, 1, nil
	case strings.HasPrefix(query, "DELETE FROM "):
		key := string(arg(0))
		if _, ok := t.rows[key]; !ok {
			return nil, 0, nil
		}
		delete(t.rows, key)
		return nil, 1, nil
	}
	return nil, 0, fmt.Errorf("fake: cannot answer %q", query)
}

// versionStatement runs one of the document store's statements on the
// versions of the table name.
func versionStatement(d *fakeData, query, name string, args []driver.NamedValue) (driver.Rows, int64, error) {
	switch {
	case strings.HasPrefix(query, "CREATE TABLE IF NOT EXISTS "):
		if d.vs[name] == nil {
			d.vs[name] = map[string]map[int64]fakeVersion{}
		}
		return nil, 0, nil
	case strings.HasPrefix(query, "DROP TABLE IF EXISTS "):
		delete(d.vs, name)
		return nil, 0, nil
	}
	table := d.vs[name]
	if table == nil {
		return nil, 0, fmt.Errorf("fake: no table %s___vs", name)
	}
	key := string(bound(args[0].Value))
	rows := table[key]
	nums := slices.Sorted(maps.Keys(rows))
	slices.Reverse(nums)
	formers := slices.DeleteFunc(slices.Clone(nums), func(n int64) bool { return rows[n].doc == nil })
	switch {
	case strings.HasPrefix(query, "INSERT INTO "):
		var n int64
		for i := 0; i+5 < len(args); i += 6 {
			k := string(bound(args[i].Value))
			if table[k] == nil {
				table[k] = map[int64]fakeVersion{}
			}
			num := args[i+1].Value.(int64)
			if _, taken := table[k][num]; taken {
				return nil, 0, errors.New("fake: duplicate key in the versions' table")
			}
			v := fakeVersion{at: args[i+2].Value, ns: args[i+3].Value}
			if b := bound(args[i+4].Value); b != nil {
				v.meta = slices.Clone(b)
			}
			if b := bound(args[i+5].Value); b != nil {
				v.doc = slices.Clone(b)
			}
			table[k][num] = v
			n++
		}
		return nil, n, nil
	case strings.HasPrefix(query, "SELECT num FROM ") && strings.Contains(query, "doc IS NULL"):
		out := &fakeRows{cols: []string{"num"}}
		for _, n := range nums {
			if rows[n].doc == nil {
				out.rows = append(out.rows, []driver.Value{n})
			}
		}
		return out, 0, nil
	case strings.HasPrefix(query, "SELECT num FROM ") && strings.Contains(query, "OFFSET"):
		out := &fakeRows{cols: []string{"num"}}
		if at := int(args[1].Value.(int64)); at < len(formers) {
			out.rows = append(out.rows, []driver.Value{formers[at]})
		}
		return out, 0, nil
	case strings.HasPrefix(query, "SELECT num, made_at"):
		out := &fakeRows{cols: []string{"num", "made_at", "made_ns", "meta", "doc"}}
		for _, n := range formers {
			v := rows[n]
			out.rows = append(out.rows, []driver.Value{n, v.at, v.ns, nilOr(v.meta), slices.Clone(v.doc)})
		}
		return out, 0, nil
	case strings.HasPrefix(query, "UPDATE ") && strings.Contains(query, "SET doc ="):
		kept, num := table[boundKey(args[1].Value)], args[2].Value.(int64)
		v, ok := kept[num]
		if !ok {
			return nil, 0, nil
		}
		v.doc = slices.Clone(bound(args[0].Value))
		kept[num] = v
		return nil, 1, nil
	case strings.HasPrefix(query, "DELETE FROM ") && strings.Contains(query, "num <="):
		cut, n := args[1].Value.(int64), int64(0)
		for _, num := range formers {
			if num <= cut {
				delete(rows, num)
				n++
			}
		}
		return nil, n, nil
	case strings.HasPrefix(query, "DELETE FROM ") && strings.Contains(query, "doc IS NOT NULL"):
		for _, num := range formers {
			delete(rows, num)
		}
		return nil, int64(len(formers)), nil
	case strings.HasPrefix(query, "DELETE FROM "):
		delete(table, key)
		return nil, int64(len(nums)), nil
	}
	return nil, 0, fmt.Errorf("fake: cannot answer %q", query)
}

// readVersions answers the read of every version of a document with the
// document itself, newest first: one row without a number for a document
// stored before its store kept versions, none for no document.
func readVersions(d *fakeData, query string, args []driver.NamedValue) (driver.Rows, int64, error) {
	name := quoted.FindStringSubmatch(query)[1]
	t, table := d.docs[name], d.vs[name]
	if t == nil || table == nil {
		return nil, 0, fmt.Errorf("fake: no table %s", name)
	}
	key := boundKey(args[0].Value)
	out := &fakeRows{cols: []string{"num", "made_at", "made_ns", "meta", "doc"}}
	r, ok := t.rows[key]
	if !ok {
		return out, 0, nil
	}
	rows := table[key]
	if len(rows) == 0 {
		out.rows = append(out.rows, []driver.Value{nil, nil, nil, nil, slices.Clone(r.doc)})
		return out, 0, nil
	}
	nums := slices.Sorted(maps.Keys(rows))
	slices.Reverse(nums)
	for _, n := range nums {
		v, doc := rows[n], rows[n].doc
		if doc == nil {
			doc = r.doc
		}
		out.rows = append(out.rows, []driver.Value{n, v.at, v.ns, nilOr(v.meta), slices.Clone(doc)})
	}
	return out, 0, nil
}

// nilOr is b as a driver value: NULL when it is nil.
func nilOr(b []byte) driver.Value {
	if b == nil {
		return nil
	}
	return slices.Clone(b)
}

// docs are the documents under keys, in that order.
func (t *fakeTable) docs(keys []string) driver.Rows {
	out := &fakeRows{cols: []string{"doc"}}
	for _, k := range keys {
		out.rows = append(out.rows, []driver.Value{slices.Clone(t.rows[k].doc)})
	}
	return out
}

// indexStatement runs a statement on a table's index rows.
func indexStatement(t *fakeTable, query string, args []driver.NamedValue, arg func(int) []byte) (driver.Rows, int64, error) {
	switch {
	case strings.HasPrefix(query, "DELETE FROM ") && len(args) == 1:
		doc := string(arg(0))
		maps.DeleteFunc(t.ix, func(ix fakeIx, _ bool) bool { return ix.doc == doc })
		return nil, 1, nil
	case strings.HasPrefix(query, "DELETE FROM "):
		clear(t.ix)
		return nil, 1, nil
	case strings.HasPrefix(query, "INSERT INTO "):
		var n int64
		for i := 0; i+3 < len(args); i += 4 {
			ix := fakeIx{name: string(arg(i)), key: string(arg(i + 1)), doc: string(arg(i + 2))}
			uniq := args[i+3].Value != nil
			for other, u := range t.ix {
				if uniq && u && other.name == ix.name && other.key == ix.key && other.doc != ix.doc {
					return nil, 0, errors.New("fake: duplicate key in a unique index")
				}
			}
			t.ix[ix] = uniq
			n++
		}
		return nil, n, nil
	case strings.HasPrefix(query, "UPDATE ") && strings.Contains(query, "SET uniq = 1"):
		for ix := range t.ix {
			if slices.ContainsFunc(args, func(a driver.NamedValue) bool { return bytes.Equal(bound(a.Value), []byte(ix.name)) }) {
				t.ix[ix] = true
			}
		}
		return nil, 1, nil
	case strings.HasPrefix(query, "SELECT index_name FROM ") && strings.Contains(query, "HAVING"):
		count := map[[2]string]int{}
		for ix := range t.ix {
			count[[2]string{ix.name, ix.key}]++
		}
		out := &fakeRows{cols: []string{"index_name"}}
		for k, n := range count {
			if n > 1 && slices.ContainsFunc(args, func(a driver.NamedValue) bool { return bytes.Equal(bound(a.Value), []byte(k[0])) }) {
				out.rows = append(out.rows, []driver.Value{[]byte(k[0])})
			}
		}
		return out, 0, nil
	case strings.HasPrefix(query, "SELECT index_name FROM "):
		own := string(arg(0))
		out := &fakeRows{cols: []string{"index_name"}}
		for i := 1; i+1 < len(args); i += 2 {
			for ix, uniq := range t.ix {
				if uniq && ix.doc != own && ix.name == string(arg(i)) && ix.key == string(arg(i+1)) {
					out.rows = append(out.rows, []driver.Value{[]byte(ix.name)})
				}
			}
		}
		return out, 0, nil
	}
	return nil, 0, fmt.Errorf("fake: cannot answer %q", query)
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
