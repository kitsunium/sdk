package kit_test

import (
	"context"
	"net/http"
	"net/url"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// Databases (ADR 0004, step 1), on kit's fake database: no network, no
// driver. The engine modules run the same on PostgreSQL and MySQL. This file
// holds the product the tests run and a database opening, answering and
// drawn; database_*_test.go the rest.

type entry struct {
	ID string `json:"id"`
}

func (e entry) key() string { return e.ID }

// ledger is a product of two services, declared afresh for each test: a
// service runs in one app at a time.
type ledger struct {
	books, audit          *kit.Service
	entries, cache, trail *kit.StoreService[entry]
}

func newLedger() ledger {
	l := ledger{
		books: kit.NewService("books", "The books."),
		audit: kit.NewService("audit", "The audit trail."),
	}
	l.entries = l.books.Store("entries", entry.key)
	l.cache = l.books.Store("cache", entry.key, kit.InMemory())
	l.trail = l.audit.Store("trail", entry.key)
	return l
}

// here is the line that calls it.
func here() int {
	_, _, line, _ := runtime.Caller(1)
	return line
}

// migrations are the product's: two tables.
var migrations = []sql.Migration{
	{Version: 20260901120000, Name: "create entries", Up: sql.Statements("CREATE TABLE entries (id TEXT)"), Down: sql.Statements("DROP TABLE entries")},
	{Version: 20260902120000, Name: "create trail", Up: sql.Statements("CREATE TABLE trail (id TEXT)"), Down: sql.Statements("DROP TABLE trail")},
}

// fakeURL is a URL of kit's fake engine, for the database ledger on
// db.internal:5432, as user with word — built, never written out: a URL
// holding its credentials is what a scanner looks for.
func fakeURL(user, word, query string) string {
	return (&url.URL{Scheme: "fake", User: url.UserPassword(user, word), Host: "db.internal:5432", Path: "/ledger", RawQuery: query}).String()
}

// verifiedURL is the ledger's usual URL: TLS verified.
func verifiedURL() string { return fakeURL("ledger", "pw", "tls=verify-full") }

// ledgerApp is the ledger in production, its data in a directory of the
// test's, its secrets in memory.
func ledgerApp(t *testing.T, l ledger, logs *syncBuffer, opts ...kit.AppConfigurer) *kit.App {
	t.Helper()
	t.Setenv("KIT_SECRETS", "memory")
	return kit.NewApp("ledger", l.books, l.audit).With(append([]kit.AppConfigurer{
		kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvProduction), kit.DataDir(t.TempDir()), kit.Logs(logs),
	}, opts...)...)
}

// run starts app and stops it with the test.
func run(t *testing.T, app *kit.App) {
	t.Helper()
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := app.Stop(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

// databaseOf is the runtime's database named name.
func databaseOf(t *testing.T, g *model.Graph, name string) model.Database {
	t.Helper()
	if g.Runtime != nil {
		for _, d := range g.Runtime.Databases {
			if d.Name == name {
				return d
			}
		}
	}
	t.Fatalf("no database %q in the runtime", name)
	return model.Database{}
}

// containerByID is the architecture's container id.
func containerByID(g *model.Graph, id string) *model.Container {
	for i := range g.Architecture.Containers {
		if c := &g.Architecture.Containers[i]; c.ID == id {
			return c
		}
	}
	return nil
}

// linkTo is the architecture's link to id.
func linkTo(g *model.Graph, id string) *model.Link {
	for i := range g.Architecture.Links {
		if l := &g.Architecture.Links[i]; l.To == id {
			return l
		}
	}
	return nil
}

// warns reports whether the graph has a warning saying all of says.
func warns(g *model.Graph, says ...string) bool {
	return slices.ContainsFunc(g.Diagnostics, func(d model.Diagnostic) bool {
		return d.Severity == "warning" && !slices.ContainsFunc(says, func(s string) bool { return !strings.Contains(d.Message, s) })
	})
}

// opened is the ledger run on a database that opens, migrates and answers.
type opened struct {
	app  *kit.App
	db   *kit.FakeDB
	g    *model.Graph
	logs *syncBuffer
	line int
}

// openLedger runs the ledger on a fake PostgreSQL database with the
// product's migrations, declared at the line it returns.
func openLedger(t *testing.T) opened {
	t.Helper()
	o := opened{db: kit.NewFakeDB(sql.DialectPostgres), logs: &syncBuffer{}}
	t.Setenv("LEDGER_DATABASE_URL", verifiedURL())
	opt, line := kit.Database("database", o.db.Engine(), kit.Migrations(migrations...)), here()
	o.app, o.line = ledgerApp(t, newLedger(), o.logs, opt), line
	run(t, o.app)
	o.g = o.app.Graph()
	return o
}

// A database opens at start, runs the product's migrations under their own
// version table, and answers its check.
func TestADatabaseOpensAndMigrates(t *testing.T) {
	needsFileStore(t)
	o := openLedger(t)
	if got := o.db.Versions("schema_migrations"); !slices.Equal(got, []int64{20260901120000, 20260902120000}) {
		t.Errorf("schema_migrations holds %v", got)
	}
	if got := o.db.Executed(); !slices.Equal(got, []string{"CREATE TABLE entries (id TEXT)", "CREATE TABLE trail (id TEXT)"}) {
		t.Errorf("executed %v", got)
	}
	rt := databaseOf(t, o.g, "database")
	type seen struct {
		state, engine, from, tls string
		ready, checked           bool
	}
	if got := (seen{rt.State, rt.Engine, rt.URLFrom, rt.TLS, rt.Ready, rt.CheckedAt != nil}); got != (seen{model.DatabaseOpen, "postgres", model.SettingEnv, "verify-full", true, true}) {
		t.Errorf("runtime %+v", rt)
	}
	if rt.Pool == nil || rt.Pool.MaxOpen != 10 {
		t.Errorf("pool %+v", rt.Pool)
	}
	want := model.MigrationSet{Name: "product", Table: "schema_migrations", Applied: []model.Migration{
		{Version: 20260901120000, Name: "create entries"}, {Version: 20260902120000, Name: "create trail"},
	}}
	if len(rt.Migrations) != 1 || !sameSet(rt.Migrations[0], want) {
		t.Errorf("migrations %+v", rt.Migrations)
	}
}

// sameSet reports whether two migration sets say the same.
func sameSet(x, y model.MigrationSet) bool {
	return x.Name == y.Name && x.Table == y.Table && x.Problem == y.Problem &&
		slices.Equal(x.Applied, y.Applied) && slices.Equal(x.Pending, y.Pending)
}

// A database is a lifecycle component after the secrets and before the
// stores it keeps.
func TestADatabaseStartsBeforeItsStores(t *testing.T) {
	needsFileStore(t)
	o := openLedger(t)
	var order []string
	for _, c := range o.g.Runtime.Components {
		order = append(order, c.Name)
	}
	if !before(order, "database:database", "store:books/store/entries") {
		t.Errorf("components %v", order)
	}
	if r := call(t, o.app, "GET /_kit/health/ready", noBody); r.status != http.StatusOK {
		t.Errorf("ready: %d %s", r.status, r.body)
	}
	if strings.Contains(o.logs.String(), "pw@") {
		t.Error("the logs show the URL")
	}
}

// before reports whether first comes before then in list, both there.
func before(list []string, first, then string) bool {
	i, j := slices.Index(list, first), slices.Index(list, then)
	return i >= 0 && j >= 0 && i < j
}

// The default database keeps every store but a cache, whose data stays in
// the data directory in this version of kit — and the start says so.
func TestTheDefaultDatabaseKeepsEveryStore(t *testing.T) {
	needsFileStore(t)
	o := openLedger(t)
	for id, want := range map[string]model.StoreInfo{
		"books/store/entries": {Backend: "file", Database: "database"},
		"audit/store/trail":   {Backend: "file", Database: "database"},
		"books/store/cache":   {Backend: "memory"},
	} {
		if got := o.g.Node(id).Store; got.Backend != want.Backend || got.Database != want.Database {
			t.Errorf("%s: %+v", id, got)
		}
	}
	if !warns(o.g, `database "database" keeps 2 store(s)`) {
		t.Errorf("the start does not say the stores stay in the data directory: %+v", o.g.Diagnostics)
	}
}

// A database is drawn: its container — its engine, its technology, its
// settings, its declaration, no location outside dev, no store while their
// data stays in files.
func TestADatabaseIsDrawn(t *testing.T) {
	needsFileStore(t)
	o := openLedger(t)
	c := containerByID(o.g, "container:database:database")
	if c == nil {
		t.Fatal("no container for the database")
	}
	type seen struct{ kind, engine, technology, location string }
	if got := (seen{c.Kind, c.Engine, c.Technology, c.Location}); got != (seen{model.ContainerDatabase, "postgres", "PostgreSQL · fake · TLS verify-full", ""}) || len(c.Nodes) != 0 {
		t.Errorf("container %+v", c)
	}
	if want := (model.Source{File: "internal/kit/database_test.go", Line: o.line}); c.Source == nil || *c.Source != want || !slices.Contains(o.g.Files(), model.File{File: want.File}) {
		t.Errorf("the container points at %+v, want %+v", c.Source, want)
	}
	if want := []string{
		"LEDGER_DATABASE_URL", "LEDGER_DATABASE_MAX_OPEN", "LEDGER_DATABASE_MAX_IDLE", "LEDGER_DATABASE_MAX_LIFETIME",
		"LEDGER_DATABASE_MAX_IDLE_TIME", "LEDGER_DATABASE_TIMEOUT", "LEDGER_DATABASE_CHECK_TIMEOUT", "LEDGER_DATABASE_MIGRATE",
	}; !slices.Equal(c.Settings, want) {
		t.Errorf("settings %v", c.Settings)
	}
}

// The process's link to a database carries the database connector; the
// data directory, which holds the stores the database keeps for now, is no
// database.
func TestADatabasesLink(t *testing.T) {
	needsFileStore(t)
	o := openLedger(t)
	if l := linkTo(o.g, "container:database:database"); l == nil || l.From != "container:process" || l.Label != "Reads and writes" ||
		!slices.Equal(l.Connectors, []string{model.ConnectorDatabase}) {
		t.Errorf("link %+v", l)
	}
	if l := linkTo(o.g, "container:volume"); l == nil || slices.Contains(l.Connectors, model.ConnectorDatabase) {
		t.Errorf("the link to the data directory: %+v", l)
	}
	if !slices.ContainsFunc(o.g.Connectors, func(c model.Connector) bool {
		return c.ID == model.ConnectorDatabase && slices.Equal(c.Nodes, []string{"audit/store/trail", "books/store/entries"})
	}) {
		t.Errorf("connectors %+v", o.g.Connectors)
	}
}

// Readiness follows the database; liveness never depends on it: a database
// outage must not restart every process.
func TestReadinessFollowsTheDatabase(t *testing.T) {
	needsFileStore(t)
	db := kit.NewFakeDB(sql.DialectPostgres)
	t.Setenv("LEDGER_DATABASE_URL", verifiedURL())
	var logs syncBuffer
	app := ledgerApp(t, newLedger(), &logs, kit.Database("database", db.Engine()))
	run(t, app)
	db.SetDown(true)
	db.Retire()
	if r := call(t, app, "GET /_kit/health/ready", noBody); r.status != http.StatusServiceUnavailable {
		t.Errorf("ready while the database is down: %d", r.status)
	}
	if r := call(t, app, "GET /_kit/health/live", noBody); r.status != http.StatusOK {
		t.Errorf("live while the database is down: %d", r.status)
	}
	if d := databaseOf(t, app.Graph(), "database"); d.Ready || d.Problem == "" {
		t.Errorf("runtime while down %+v", d)
	}
	db.SetDown(false)
	if r := call(t, app, "GET /_kit/health/ready", noBody); r.status != http.StatusOK {
		t.Errorf("ready once the database answers: %d %s", r.status, r.body)
	}
	if d := databaseOf(t, app.Graph(), "database"); !d.Ready || d.Problem != "" {
		t.Errorf("runtime once back %+v", d)
	}
}

// A database that does not answer fails the start, which unwinds what
// started.
func TestADatabaseThatDoesNotAnswerFailsTheStart(t *testing.T) {
	needsFileStore(t)
	db := kit.NewFakeDB(sql.DialectPostgres)
	db.SetDown(true)
	t.Setenv("LEDGER_DATABASE_URL", verifiedURL())
	var logs syncBuffer
	l := newLedger()
	err := ledgerApp(t, l, &logs, kit.Database("database", db.Engine())).Start(t.Context())
	if !errs.HasCode(err, kit.CodeDatabaseUnavailable) {
		t.Fatalf("start: %v", err)
	}
	if _, err := l.entries.List(t.Context()); err == nil {
		t.Error("a store runs after the start failed")
	}
}
