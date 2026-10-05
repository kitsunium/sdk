package kit

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// The sets of migrations, and their version tables.
const (
	setProduct   = "product"
	tableProduct = "schema_migrations"
)

// maxPublic is the most runes an SDK error's public text holds.
const maxPublic int = 120

// A database's migrations (ADR 0004) come in sets, one per origin — kit's
// own, the product's (kit.Migrations), a module's —, each with its own
// version table, and so its own lock. They run when the database starts
// (<name>-migrate: start) or by `<product> migrate` (migratecmd.go), under
// the SDK's Migrator; kit sends no SQL of its own.

// migrationSet is one set of migrations a database runs, with its own
// version table — and so its own lock.
type migrationSet struct {
	// name is the set's origin: "kit", "product", a module's.
	name       string
	table      string
	migrations []sql.Migration
}

// migrateAtStart runs a database's sets, in order — kit's own first, which
// make the tables of the stores it keeps —, when it migrates at its start;
// when it migrates manually, a pending migration refuses the start, naming
// the command that runs it. It returns each set as it left it, and kit's
// registry of its tables.
func (a *App) migrateAtStart(ctx context.Context, d *database, o *opened) ([]model.MigrationSet, *tableRegistry, error) {
	manual := resolvedSetting(a, d.migrate) == migrateManual
	var out []model.MigrationSet
	sets := a.migrationSets(d)
	var registry *tableRegistry
	if len(a.kitTables(d)) > 0 {
		kit, g, err := a.kitSetAtStart(ctx, d, o, manual)
		registry = g
		if err != nil {
			return out, registry, a.migrationFailure(d, migrationSet{name: setKit, table: tableKit}, err)
		}
		sets = append([]migrationSet{kit}, sets...)
	}
	for _, set := range sets {
		left, err := a.migrateSet(ctx, d, o, set, manual)
		if left != nil {
			out = append(out, *left)
		}
		if err != nil {
			return out, registry, err
		}
	}
	return out, registry, nil
}

// migrateSet runs one of d's sets at its start — applies it, or, when d
// migrates manually, refuses the start while it is pending — and returns it
// as it left it, nil when it could not read it.
func (a *App) migrateSet(ctx context.Context, d *database, o *opened, set migrationSet, manual bool) (*model.MigrationSet, error) {
	m, err := sql.NewMigrator(o.cfg, sql.MigrateConfig{Migrations: set.migrations, VersionTable: set.table})
	if err != nil {
		return nil, a.migrationFailure(d, set, err)
	}
	if manual {
		return a.pendingSet(ctx, d, set, m)
	}
	if set.name != setKit {
		if err := m.Up(ctx); err != nil {
			return new(readSet(ctx, set, m)), a.migrationFailure(d, set, err)
		}
	}
	return new(readSet(ctx, set, m)), nil
}

// pendingSet refuses the start of d, which migrates manually, while set has
// a pending migration, naming the command that runs it.
func (a *App) pendingSet(ctx context.Context, d *database, set migrationSet, m sql.Migrator) (*model.MigrationSet, error) {
	pending, err := m.Plan(ctx)
	if err != nil {
		return nil, a.migrationFailure(d, set, err)
	}
	if len(pending) > 0 {
		return new(setStatus(set, pending)), dbFailure(CodeDatabaseMigrate, "DATABASE_MIGRATIONS_PENDING",
			fmt.Sprintf("database %q has %d pending migration(s) of its %s set and migrates manually: run `%s migrate up`", d.name, len(pending), set.name, a.name),
			errs.String("database", d.name), errs.String("set", set.name))
	}
	return new(readSet(ctx, set, m)), nil
}

// kitSetAtStart is kit's set on d at its start: applied — its registry, then
// its tables —, or, when d migrates manually, as its registry says it, for
// the start to judge.
func (a *App) kitSetAtStart(ctx context.Context, d *database, o *opened, manual bool) (migrationSet, *tableRegistry, error) {
	g, err := openRegistry(ctx, o.cfg, o.tm, !manual)
	if err != nil {
		return migrationSet{name: setKit, table: tableKit}, nil, err
	}
	if manual {
		set, err := a.planKitSet(d, g)
		return set, g, err
	}
	set, err := a.applyKitSet(ctx, d, o.cfg, g)
	return set, g, err
}

// migrationFailure is a set that did not apply, in kit's words: the SDK's
// verdict and the version it names, never the driver's.
func (a *App) migrationFailure(d *database, set migrationSet, err error) error {
	fields := []errs.Field{errs.String("database", d.name), errs.String("set", set.name)}
	where := fmt.Sprintf("database %q, %s migrations", d.name, set.name)
	if v := fieldOf(err, "version"); v != "" {
		where = fmt.Sprintf("database %q, %s migration %s", d.name, set.name, v)
		fields = append(fields, errs.String("version", v))
	}
	return dbFailure(CodeDatabaseMigrate, "DATABASE_MIGRATE", where+": "+errs.PublicOf(err), fields...)
}

// dbFailure is a database's failure: kit's code, a sentence bounded to what
// an SDK error's public text holds, and its fields — never a driver's words.
func dbFailure(code errs.Code, reason, text string, fields ...errs.Field) error {
	if utf8.RuneCountInString(text) > maxPublic {
		text = string([]rune(text)[:maxPublic-1]) + "…"
	}
	return failure(code, reason, text, nil, fields...)
}

// readSet says what a set's version table records and what this build has
// that it does not, as the SDK's migrator reads it.
func readSet(ctx context.Context, set migrationSet, m sql.Migrator) model.MigrationSet {
	pending, err := m.Plan(ctx)
	if err != nil {
		return model.MigrationSet{Name: set.name, Table: set.table, Problem: errs.PublicOf(err)}
	}
	return setStatus(set, pending)
}

// setStatus is a set, oldest first: what its version table records — the
// migrations of the set the SDK's Plan does not find pending — and what is
// pending. kit reads no version table itself: its columns are the SDK's.
func setStatus(set migrationSet, pending []sql.Migration) model.MigrationSet {
	out := model.MigrationSet{Name: set.name, Table: set.table}
	waiting := map[uint64]bool{}
	for _, p := range pending {
		waiting[p.Version] = true
		out.Pending = append(out.Pending, model.Migration{Version: p.Version, Name: p.Name})
	}
	for _, m := range set.migrations {
		if !waiting[m.Version] {
			out.Applied = append(out.Applied, model.Migration{Version: m.Version, Name: m.Name})
		}
	}
	slices.SortFunc(out.Applied, func(x, y model.Migration) int { return cmp.Compare(x.Version, y.Version) })
	return out
}
