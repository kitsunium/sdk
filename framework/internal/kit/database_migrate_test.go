package kit_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// A database's migrations: at start, manually, and through the product's
// migrate command.

// manualLedger is the ledger on db, migrating manually, a new app at each
// call: a service runs in one app at a time.
func manualLedger(t *testing.T, db *kit.FakeDB) func() *kit.App {
	t.Helper()
	t.Setenv("LEDGER_DATABASE_URL", verifiedURL())
	t.Setenv("LEDGER_DATABASE_MIGRATE", "manual")
	var logs syncBuffer
	return func() *kit.App {
		return ledgerApp(t, newLedger(), &logs, kit.Database("database", db.Engine(), kit.Migrations(migrations...)))
	}
}

// migrateCommand runs the migrate command of a new app, and returns its
// status and what it printed.
func migrateCommand(t *testing.T, newApp func() *kit.App, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := newApp().MigrateCommand(t.Context(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// With <name>-migrate: manual, pending migrations refuse the start, naming
// the command that runs them.
func TestManualMigrationsRefuseTheStart(t *testing.T) {
	needsFileStore(t)
	db := kit.NewFakeDB(sql.DialectPostgres)
	err := manualLedger(t, db)().Start(t.Context())
	if !errs.HasCode(err, kit.CodeDatabaseMigrate) || !strings.Contains(err.Error(), "ledger migrate up") {
		t.Fatalf("a manual database with pending migrations started: %v", err)
	}
	if len(db.Versions("schema_migrations")) != 0 {
		t.Fatal("a manual database migrated at start")
	}
}

// `migrate status` says where each set is, and `migrate up` runs what is
// pending, once.
func TestMigrateStatusAndUp(t *testing.T) {
	newApp := manualLedger(t, kit.NewFakeDB(sql.DialectPostgres))
	code, out, errOut := migrateCommand(t, newApp, "status")
	// kit's own set first — its registry, the two stores' tables —, then
	// the product's.
	if code != 0 || strings.Count(out, "pending") != 5 || strings.Count(out, "kit_migrations") != 3 || !strings.Contains(out, "schema_migrations") {
		t.Fatalf("status: %d\n%s%s", code, out, errOut)
	}
	if code, out, errOut = migrateCommand(t, newApp, "up"); code != 0 || !strings.Contains(out, `database/kit: applied 1 "docstore kit_tables"`) ||
		!strings.Contains(out, `database/product: applied 20260901120000 "create entries", 20260902120000 "create trail"`) {
		t.Fatalf("up: %d\n%s%s", code, out, errOut)
	}
	if code, out, _ = migrateCommand(t, newApp, "up"); code != 0 || !strings.Contains(out, "database/kit: up to date") ||
		!strings.Contains(out, "database/product: up to date") {
		t.Fatalf("up again: %d\n%s", code, out)
	}
	if code, out, _ = migrateCommand(t, newApp, "status"); code != 0 || strings.Count(out, "applied") != 5 {
		t.Fatalf("status after up: %d\n%s", code, out)
	}
	run(t, newApp())
}

// kit's own set is not migrated down: its migrations drop the stores'
// tables, and every entity in them.
func TestKitsOwnSetIsNotMigratedDown(t *testing.T) {
	newApp := manualLedger(t, kit.NewFakeDB(sql.DialectPostgres))
	if code, _, errOut := migrateCommand(t, newApp, "down", "kit", "1"); code != 2 || !strings.Contains(errOut, "kit's own set is not migrated down") {
		t.Errorf("down of kit's set: %d %s", code, errOut)
	}
}

// `migrate down SET VERSION` reverses the set's migrations above the
// version, newest first.
func TestMigrateDown(t *testing.T) {
	needsFileStore(t)
	db := kit.NewFakeDB(sql.DialectPostgres)
	newApp := manualLedger(t, db)
	if code, _, errOut := migrateCommand(t, newApp, "up"); code != 0 {
		t.Fatalf("up: %d %s", code, errOut)
	}
	run(t, newApp())
	code, out, errOut := migrateCommand(t, newApp, "down", "product", "20260901120000")
	if code != 0 || !strings.Contains(out, `reversed 20260902120000 "create trail"`) {
		t.Fatalf("down: %d\n%s%s", code, out, errOut)
	}
	if got := db.Versions("schema_migrations"); !slices.Equal(got, []int64{20260901120000}) {
		t.Errorf("after down: %v", got)
	}
}

// The command refuses what it cannot do: an unknown set, an unknown
// subcommand.
func TestTheMigrateCommandRefusesItsMistakes(t *testing.T) {
	newApp := manualLedger(t, kit.NewFakeDB(sql.DialectPostgres))
	if code, _, errOut := migrateCommand(t, newApp, "down", "nothing", "1"); code != 2 || !strings.Contains(errOut, "the sets are database/product") {
		t.Errorf("down of an unknown set: %d %s", code, errOut)
	}
	if code, _, errOut := migrateCommand(t, newApp, "down", "product", "yesterday"); code != 2 || !strings.Contains(errOut, "is not a version") {
		t.Errorf("down to no version: %d %s", code, errOut)
	}
	if code, _, _ := migrateCommand(t, newApp, "sideways"); code != 2 {
		t.Errorf("an unknown command: %d", code)
	}
}

// A migration that fails stops the start, naming its version in kit's
// words, never the driver's.
func TestAFailedMigrationStopsTheStart(t *testing.T) {
	needsFileStore(t)
	db := kit.NewFakeDB(sql.DialectPostgres)
	t.Setenv("LEDGER_DATABASE_URL", verifiedURL())
	var logs syncBuffer
	broken := append(slices.Clone(migrations), sql.Migration{Version: 20260903120000, Name: "broken", Up: sql.Statements("FAIL here"), Down: sql.Irreversible})
	err := ledgerApp(t, newLedger(), &logs, kit.Database("database", db.Engine(), kit.Migrations(broken...))).Start(t.Context())
	if !errs.HasCode(err, kit.CodeDatabaseMigrate) || !strings.Contains(err.Error(), "20260903120000") || strings.Contains(err.Error(), "syntax") {
		t.Fatalf("start: %v", err)
	}
	if got := db.Versions("schema_migrations"); !slices.Equal(got, []int64{20260901120000, 20260902120000}) {
		t.Errorf("schema_migrations holds %v", got)
	}
}
