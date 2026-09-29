// Package kit — the migrations a database runs, and their lock.
package kit

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/sql"
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

// migrateAtStart runs a database's sets, in order, when it migrates at its
// start; when it migrates manually, a pending migration refuses the start,
// naming the command that runs it. It returns each set as it left it.
func (a *App) migrateAtStart(ctx context.Context, d *database, cfg *sql.Config) ([]model.MigrationSet, error) {
	manual := resolvedSetting(a, d.migrate) == migrateManual
	var out []model.MigrationSet
	for _, set := range a.migrationSets(d) {
		m, err := sql.NewMigrator(*cfg, sql.MigrateConfig{Migrations: set.migrations, VersionTable: set.table})
		if err != nil {
			return out, a.migrationFailure(d, set, err)
		}
		if manual {
			pending, err := m.Plan(ctx)
			if err != nil {
				return out, a.migrationFailure(d, set, err)
			}
			if len(pending) > 0 {
				out = append(out, setStatus(set, pending))
				return out, dbFailure(CodeDatabaseMigrate, "DATABASE_MIGRATIONS_PENDING",
					fmt.Sprintf("database %q has %d pending migration(s) of its %s set and migrates manually: run `%s migrate up`", d.name, len(pending), set.name, a.name),
					errs.String("database", d.name), errs.String("set", set.name))
			}
		} else if err := m.Up(ctx); err != nil {
			out = append(out, readSet(ctx, set, m))
			return out, a.migrationFailure(d, set, err)
		}
		out = append(out, readSet(ctx, set, m))
	}
	return out, nil
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
