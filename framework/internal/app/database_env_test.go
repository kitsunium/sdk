package app_test

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// Databases in their environments: without a URL, in dev and outside it;
// the TLS rule; SQLite beside the data; an app in memory; where each store
// lives.

// The URL is required outside dev: the start fails, naming the variable.
func TestAURLIsRequiredOutsideDev(t *testing.T) {
	db := app.NewFakeDB(sql.DialectPostgres)
	var logs syncBuffer
	err := ledgerApp(t, newLedger(), &logs, app.Database("database", db.Engine())).Start(t.Context())
	var de *app.DiagnosticsError
	if !errors.As(err, &de) || !strings.Contains(err.Error(), "LEDGER_DATABASE_URL") {
		t.Fatalf("start without a URL in production: %v", err)
	}
}

// In dev a database without a URL opens nothing and leaves its stores in
// the data directory, and says so.
func TestInDevADatabaseWithoutURLLeavesItsStoresInFiles(t *testing.T) {
	needsFileStore(t)
	db := app.NewFakeDB(sql.DialectPostgres)
	var logs syncBuffer
	l := newLedger()
	a := ledgerApp(t, l, &logs, app.Database("database", db.Engine()), app.Env(app.EnvDev), app.Analyze(false))
	run(t, a)
	g := a.Graph()
	if d := databaseOf(t, g, "database"); d.State != model.DatabaseUnset || d.Pool != nil {
		t.Errorf("runtime %+v", d)
	}
	if n := g.Node("books/store/entries"); n.Store.Backend != "file" || n.Store.Database != "database" {
		t.Errorf("store %+v", n.Store)
	}
	if !warns(g, "its stores stay in the data directory", "LEDGER_DATABASE_URL") {
		t.Errorf("dev does not say where the stores stay: %+v", g.Diagnostics)
	}
	if len(db.URLs()) != 0 {
		t.Error("a database without a URL was opened")
	}
	if err := l.entries.Put(t.Context(), entry{ID: "e1"}); err != nil {
		t.Errorf("a store of a database without a URL: %v", err)
	}
	if r := call(t, a, "GET /_app/health/ready", noBody); r.status != http.StatusOK {
		t.Errorf("ready: %d %s", r.status, r.body)
	}
}

// Outside dev a networked database's URL writes its TLS mode: the driver's
// default may fall back to plaintext.
func TestTLSLeftToTheDriverIsRefusedOutsideDev(t *testing.T) {
	db := app.NewFakeDB(sql.DialectPostgres)
	var logs syncBuffer
	t.Setenv("LEDGER_DATABASE_URL", fakeURL("ledger", "pw", ""))
	err := ledgerApp(t, newLedger(), &logs, app.Database("database", db.Engine())).Start(t.Context())
	if err == nil || !strings.Contains(err.Error(), "leaves TLS to the driver") {
		t.Fatalf("a URL leaving TLS to the driver started: %v", err)
	}
}

// TLS off is accepted when the URL writes it, for a private network, and
// the diagram says so.
func TestTLSOffIsAcceptedWhenWritten(t *testing.T) {
	needsFileStore(t)
	db := app.NewFakeDB(sql.DialectPostgres)
	var logs syncBuffer
	t.Setenv("LEDGER_DATABASE_URL", fakeURL("ledger", "pw", "tls=off"))
	a := ledgerApp(t, newLedger(), &logs, app.Database("database", db.Engine()))
	run(t, a)
	g := a.Graph()
	if c := containerByID(g, "container:database:database"); c.Technology != "PostgreSQL · fake · no TLS" {
		t.Errorf("technology %q", c.Technology)
	}
	if d := databaseOf(t, g, "database"); d.TLS != "none" {
		t.Errorf("tls %q", d.TLS)
	}
}

// A SQLite database without a URL is <data>/<name>.sqlite, and the
// configuration says where: kit's choice, and no secret.
func TestSQLiteLandsBesideTheData(t *testing.T) {
	needsFileStore(t)
	db := app.NewFakeDB(sql.DialectSQLite)
	var logs syncBuffer
	data := t.TempDir()
	a := ledgerApp(t, newLedger(), &logs, app.Database("archive", db.Engine()), app.DataDir(data))
	run(t, a)
	file := filepath.Join(data, "archive.sqlite")
	if urls := db.URLs(); len(urls) == 0 || urls[0] != file {
		t.Fatalf("opened %v", urls)
	}
	g := a.Graph()
	if d := databaseOf(t, g, "archive"); d.URLFrom != model.SettingDefault || d.Engine != "sqlite" || d.TLS != "" {
		t.Errorf("runtime %+v", d)
	}
	want := model.Setting{Name: "LEDGER_ARCHIVE_URL", Value: "file:" + file, From: model.SettingDefault, Key: "archive-url", Database: "archive"}
	if got := settingNamed(t, g, "LEDGER_ARCHIVE_URL"); got != want {
		t.Errorf("config %+v", got)
	}
}

// settingNamed is the runtime's setting read from the variable name.
func settingNamed(t *testing.T, g *model.Graph, name string) model.Setting {
	t.Helper()
	for _, s := range g.Runtime.Config {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no setting %s", name)
	return model.Setting{}
}

// app.InMemory() on the app opens no database and needs no URL: a test uses
// the product's own app.
func TestInMemoryOpensNoDatabase(t *testing.T) {
	db := app.NewFakeDB(sql.DialectPostgres)
	var logs syncBuffer
	a := ledgerApp(t, newLedger(), &logs, app.Database("database", db.Engine(), app.Migrations(migrations...)), app.InMemory())
	run(t, a)
	g := a.Graph()
	if d := databaseOf(t, g, "database"); d.State != model.DatabaseMemory {
		t.Errorf("runtime %+v", d)
	}
	if len(db.URLs()) != 0 || len(db.Versions("schema_migrations")) != 0 {
		t.Error("an app in memory opened its database")
	}
	if n := g.Node("books/store/entries"); n.Store.Backend != "memory" || n.Store.Database != "database" {
		t.Errorf("store %+v", n.Store)
	}
	for _, c := range g.Runtime.Components {
		if strings.HasPrefix(c.Name, "database:") {
			t.Errorf("a component for a database in memory: %s", c.Name)
		}
	}
}

// The most precise wins: a store kept by name, then its service, then the
// default database.
func TestTheMostPreciseKeepWins(t *testing.T) {
	needsFileStore(t)
	a, b, c := app.NewFakeDB(sql.DialectPostgres), app.NewFakeDB(sql.DialectPostgres), app.NewFakeDB(sql.DialectPostgres)
	var logs syncBuffer
	l := newLedger()
	prod := ledgerApp(t, l, &logs,
		app.Database("main", a.Engine()),
		app.Database("archive", b.Engine(), app.Keeps(l.audit)),
		app.Database("hot", c.Engine(), app.Keeps(l.trail)),
		app.Env(app.EnvDev), app.Analyze(false))
	run(t, prod)
	g := prod.Graph()
	for id, want := range map[string]string{"books/store/entries": "main", "audit/store/trail": "hot", "books/store/cache": ""} {
		if got := g.Node(id).Store.Database; got != want {
			t.Errorf("%s is kept by %q, want %q", id, got, want)
		}
	}
}
