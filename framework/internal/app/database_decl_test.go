package app_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// Databases as the app declares them: every problem at once, and the
// tuning kit declares for each.

// declarationProblems are what TestDatabaseDeclarationsAreCheckedAllAtOnce
// declares wrong, as the start says each.
var declarationProblems = []string{
	`databases "one" and "two" are both declared without app.Keeps`,
	`database "keeper" keeps service stray, which the app does not mount`,
	`database "keeper" keeps store stray/store/things, which the app does not mount`,
	`database "keeper" keeps store books/store/cache by name, but the store is kept in memory`,
	`service audit is kept by two databases, "keeper" and "keeper-two"`,
	`database "kit-mine": a database's name cannot start with "kit-"`,
	`database "` + strings.Repeat("x", 50) + `": a database's name is 1 to 49`,
	`database "second" needs the name "second-timeout" for its URL or its tuning, but books/setting/second-timeout already has it`,
	`database "third" needs the name "third-url" for its URL or its tuning, but audit/secret/third-url already has it`,
	`database "none" has no engine`,
	`the app declares database "one" twice`,
}

// Every declaration problem at once, each at its app.Database.
func TestDatabaseDeclarationsAreCheckedAllAtOnce(t *testing.T) {
	a, line := wronglyDeclared(t)
	var de *app.DiagnosticsError
	if err := a.Start(t.Context()); !errors.As(err, &de) {
		t.Fatalf("start: %v", err)
	}
	for _, want := range declarationProblems {
		i := slices.IndexFunc(de.Diagnostics, func(d model.Diagnostic) bool { return strings.Contains(d.Message, want) })
		if i < 0 {
			t.Errorf("no problem says %q", want)
			continue
		}
		if s := de.Diagnostics[i].Source; s == nil || s.File != "framework/internal/app/database_decl_test.go" || s.Line < line || s.Line > line+12 {
			t.Errorf("%q is said at %+v", want, s)
		}
	}
}

// wronglyDeclared is the ledger declaring its databases every wrong way
// declarationProblems says, from the line it returns on.
func wronglyDeclared(t *testing.T) (*app.App, int) {
	t.Helper()
	db, lite := app.NewFakeDB(sql.DialectPostgres), app.NewFakeDB(sql.DialectSQLite)
	var logs syncBuffer
	l := newLedger()
	stray := app.NewService("stray", "Mounted by no app.")
	strayStore := stray.Store("things", entry.key)
	l.books.Setting("second-timeout", time.Second)
	l.audit.Secret("third-url")
	line := here() + 1
	return ledgerApp(t, l, &logs,
		app.Database("one", db.Engine()),
		app.Database("two", db.Engine()),
		app.Database("keeper", db.Engine(), app.Keeps(l.audit, stray, l.cache, strayStore)),
		app.Database("keeper-two", db.Engine(), app.Keeps(l.audit)),
		app.Database("kit-mine", db.Engine(), app.Keeps()),
		app.Database(strings.Repeat("x", 50), db.Engine(), app.Keeps()),
		app.Database("second", db.Engine(), app.Keeps()),
		app.Database("third", db.Engine(), app.Keeps()),
		app.Database("lite", lite.Engine(), app.Keeps(), app.Migrations(migrations...)),
		app.Database("none", nil, app.Keeps()),
		app.Database("one", db.Engine(), app.Keeps()),
		app.InMemory()), line
}

// A database's tuning is declared settings, resolved from the environment
// like the product's and shown with the database's name; its URL is a
// secret, shown set or not, never a value.
func TestADatabasesTuningIsSettings(t *testing.T) {
	needsFileStore(t)
	db := app.NewFakeDB(sql.DialectPostgres)
	t.Setenv("LEDGER_DATABASE_URL", verifiedURL())
	t.Setenv("LEDGER_DATABASE_MAX_OPEN", "3")
	var logs syncBuffer
	a := ledgerApp(t, newLedger(), &logs, app.Database("database", db.Engine()))
	run(t, a)
	g := a.Graph()
	if d := databaseOf(t, g, "database"); d.Pool == nil || d.Pool.MaxOpen != 3 {
		t.Errorf("pool %+v", d.Pool)
	}
	for _, want := range []model.Setting{
		{Name: "LEDGER_DATABASE_MAX_OPEN", Value: "3", From: model.SettingEnv, Key: "database-max-open", Type: "int", Database: "database"},
		{Name: "LEDGER_DATABASE_MIGRATE", Value: "start", From: model.SettingDefault, Key: "database-migrate", Type: "text", Database: "database"},
		{Name: "LEDGER_DATABASE_URL", Secret: true, From: model.SettingEnv, Key: "database-url", Database: "database"},
	} {
		if got := settingNamed(t, g, want.Name); got != want {
			t.Errorf("%s: %+v", want.Name, got)
		}
	}
}

// A tuning out of its bounds refuses the start, naming each variable.
func TestBadTuningRefusesTheStart(t *testing.T) {
	db := app.NewFakeDB(sql.DialectPostgres)
	t.Setenv("LEDGER_DATABASE_URL", verifiedURL())
	t.Setenv("LEDGER_DATABASE_MAX_OPEN", "0")
	t.Setenv("LEDGER_DATABASE_MIGRATE", "sometimes")
	t.Setenv("LEDGER_DATABASE_CHECK_TIMEOUT", "0s")
	var logs syncBuffer
	err := ledgerApp(t, newLedger(), &logs, app.Database("database", db.Engine())).Start(t.Context())
	for _, want := range []string{"LEDGER_DATABASE_MAX_OPEN", "LEDGER_DATABASE_MIGRATE", "LEDGER_DATABASE_CHECK_TIMEOUT"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("a bad %s started: %v", want, err)
		}
	}
}
