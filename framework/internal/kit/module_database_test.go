package kit_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// A module on the product's databases (ADR 0008, ADR 0004): kit.Keeps
// places a module's stores, and the module's kit.Migrations run on the
// database that keeps it, under a version table of its own.

// notebook is a module of one service and two stores, declared afresh for
// each test: a service runs in one app at a time.
type notebook struct {
	svc           *kit.Service
	notes, drafts *kit.StoreService[entry]
	module        *kit.Module
}

// noteMigrations are the module's: one table.
var noteMigrations = []sql.Migration{
	{Version: 20260903120000, Name: "create notes", Up: sql.Statements("CREATE TABLE notes (id TEXT)"), Down: sql.Statements("DROP TABLE notes")},
}

func newNotebook(parts ...kit.ModuleConfigurer) notebook {
	n := notebook{svc: kit.NewService("notes", "The notes.")}
	n.notes = n.svc.Store("notes", entry.key)
	n.drafts = n.svc.Store("drafts", entry.key)
	n.module = kit.NewModule("notebook", "Notes, as a module.", append([]kit.ModuleConfigurer{n.svc}, parts...)...)
	return n
}

// withDatabases sets the URLs of the ledger's databases.
func withDatabases(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		t.Setenv("LEDGER_"+name+"_URL", verifiedURL())
	}
}

// kit.Keeps(module) keeps every store of the module; a store's service
// kept by another database wins over its module.
func TestKeepsPlacesAModule(t *testing.T) {
	needsFileStore(t)
	withDatabases(t, "DATABASE", "ARCHIVE")
	n := newNotebook()
	var logs syncBuffer
	app := ledgerApp(t, newLedger(), &logs, n.module,
		kit.Database("database", kit.NewFakeDB(sql.DialectPostgres).Engine()),
		kit.Database("archive", kit.NewFakeDB(sql.DialectPostgres).Engine(), kit.Keeps(n.module)))
	run(t, app)
	g := app.Graph()
	for id, want := range map[string]string{"notebook.notes/store/notes": "archive", "notebook.notes/store/drafts": "archive", "books/store/entries": "database"} {
		if got := g.Node(id).Store.Database; got != want {
			t.Errorf("%s is kept by %q, want %q", id, got, want)
		}
	}
}

// A module's migrations run on the database that keeps it, under
// <module>_migrations, beside the product's own.
func TestAModulesMigrationsRunOnItsDatabase(t *testing.T) {
	needsFileStore(t)
	withDatabases(t, "DATABASE", "ARCHIVE")
	n := newNotebook(kit.Migrations(noteMigrations...))
	main, archive := kit.NewFakeDB(sql.DialectPostgres), kit.NewFakeDB(sql.DialectPostgres)
	var logs syncBuffer
	app := ledgerApp(t, newLedger(), &logs, n.module,
		kit.Database("database", main.Engine(), kit.Migrations(migrations...)),
		kit.Database("archive", archive.Engine(), kit.Keeps(n.module)))
	run(t, app)
	if got := archive.Versions("notebook_migrations"); !slices.Equal(got, []int64{20260903120000}) {
		t.Errorf("notebook_migrations on the archive holds %v", got)
	}
	if got := main.Versions("notebook_migrations"); len(got) != 0 {
		t.Errorf("the module's set ran on a database that does not keep it: %v", got)
	}
	if got := main.Versions("schema_migrations"); len(got) != 2 {
		t.Errorf("the product's set: %v", got)
	}
	var sets []string
	for _, s := range databaseOf(t, app.Graph(), "archive").Migrations {
		sets = append(sets, s.Name+" "+s.Table)
	}
	// kit's own first: the module's store lives on the archive.
	if !slices.Equal(sets, []string{"kit kit_migrations", "notebook notebook_migrations"}) {
		t.Errorf("the archive's sets: %v", sets)
	}
}

// A module's migrations run on one database: a module whose stores two
// databases keep, and which declares migrations, refuses the start; so does
// kit.Keeps of a module the app does not mount.
func TestAModuleOnTwoDatabasesIsRefused(t *testing.T) {
	withDatabases(t, "DATABASE", "ARCHIVE")
	n := newNotebook(kit.Migrations(noteMigrations...))
	other := newNotebook()
	var logs syncBuffer
	err := ledgerApp(t, newLedger(), &logs, n.module,
		kit.Database("database", kit.NewFakeDB(sql.DialectPostgres).Engine()),
		kit.Database("archive", kit.NewFakeDB(sql.DialectPostgres).Engine(), kit.Keeps(n.drafts, other.module))).Start(t.Context())
	var de *kit.DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("Start = %v, want the module refused", err)
	}
	for _, said := range []string{
		`module notebook declares migrations, and two databases keep its stores, "database" and "archive"`,
		`database "archive" keeps module notebook, which the app does not mount`,
	} {
		if diagnosticSaying(de.Diagnostics, said) == nil {
			t.Errorf("no problem says %q:\n%v", said, de)
		}
	}
}
