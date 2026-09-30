// Package kit — what a module keeps: its stores and its migrations.
package kit

import (
	"slices"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// A module on the product's databases (ADR 0008, ADR 0004). A module never
// names a database: its stores run on the product's — the default one,
// unless the product places it with kit.Keeps(module) —, and the data it
// keeps outside kit's stores has its kit.Migrations, applied on the database
// that keeps it under a version table of its own, <module>_migrations.

// keep makes a module one of what kit.Keeps names: every store of its
// services.
func (m *Module) keep() kept { return kept{module: m} }

// migrations is kit.Migrations' value: an option of a database — the
// product's set — and a part of a module — the module's.
type migrations []sql.Migration

// databaseConfigure sets the option on what it configures.
func (ms migrations) databaseConfigure(o *databaseOptions) {
	o.migrations = append(o.migrations, ms...)
}

// moduleConfigure sets the option on what it configures.
func (ms migrations) moduleConfigure(m *Module) { m.migrations = append(m.migrations, ms...) }

// moduleTable is a module's version table: its name, dashes written '_',
// then _migrations — moderation_migrations.
func moduleTable(module string) string {
	return strings.ReplaceAll(module, "-", "_") + "_migrations"
}

// migrationSets are the sets a database runs after kit's own — its tables,
// under kit_migrations, which kit resolves against its registry when the
// database opens (store_sql_migrate.go) —, in order: the modules' it keeps,
// in the order they start, then the product's.
func (a *App) migrationSets(d *database) []migrationSet {
	var out []migrationSet
	for _, mm := range a.modules {
		m := mm.module
		if keepers := a.moduleKeepers(m); len(m.migrations) > 0 && len(keepers) == 1 && keepers[0] == d {
			out = append(out, migrationSet{name: m.name, table: moduleTable(m.name), migrations: m.migrations})
		}
	}
	if len(d.opts.migrations) > 0 {
		out = append(out, migrationSet{name: setProduct, table: tableProduct, migrations: d.opts.migrations})
	}
	return out
}

// moduleKeepers are the databases that keep a module's data: those its
// stores are placed on — by name, by service, by the module, by default —
// or, for a module without a store, the one that keeps it, else the default
// database.
func (a *App) moduleKeepers(m *Module) []*database {
	var out []*database
	for _, s := range a.placedStores() {
		if s.base().svc.module != m {
			continue
		}
		if p := a.placementOf(s); p.db != nil && !slices.Contains(out, p.db) {
			out = append(out, p.db)
		}
	}
	if len(out) > 0 {
		return out
	}
	var byDefault *database
	for _, d := range a.opts.databases {
		if d.keepsModule(m) {
			return []*database{d}
		}
		byDefault = firstOf(byDefault, d, !d.opts.keepsGiven)
	}
	if byDefault != nil {
		return []*database{byDefault}
	}
	return nil
}

// keepsModule reports whether d keeps module m by its name.
func (d *database) keepsModule(m *Module) bool {
	for _, k := range d.opts.keeps {
		if safeKept(k).module == m {
			return true
		}
	}
	return false
}

// moduleMigrationProblems judges the modules' migrations: a set two
// databases would run is refused, and one no database runs is said.
func (a *App) moduleMigrationProblems() []model.Diagnostic {
	var out []model.Diagnostic
	for _, mm := range a.modules {
		m := mm.module
		if len(m.migrations) == 0 {
			continue
		}
		switch keepers := a.moduleKeepers(m); len(keepers) {
		case 0:
			out = append(out, diagnosticOf("warning", "", a.source(&m.decl), say("module.migrations-nowhere", "module", m.name)))
		case 1:
		default:
			out = append(out, diagnosticOf("error", "", a.source(&m.decl),
				say("module.migrations-twice", "module", m.name, "first", keepers[0].name, "second", keepers[1].name)))
		}
	}
	return out
}
