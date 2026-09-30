// Package kit — kit's own migrations on a database.
package kit

import (
	"context"
	"errors"
	"hash/fnv"
	"maps"
	"slices"
	"strconv"
	"sync"

	"github.com/kitsunium/sdk/pkg/v1/docstore"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// kit's own migrations on a database (ADR 0004): its tables — a store's, its
// history's, a workflow's journal —, each one migration of the SDK's
// document store, under kit's version table, kit_migrations, and run before
// the modules' and the product's.
//
// The SDK's migrator refuses a pending migration older than one already
// applied, and kit cannot know when a store was declared: a table's version
// is given the first time kit creates it, and kept in kit's own registry,
// kit_tables — whose migration, version 1, comes first. A new table takes the
// next generation, above every version kit gave — its high 32 bits — and the
// digest of its name — its low ones —, so that two processes of two
// releases starting together give two new tables two versions. When one of
// them applied its own first, the other's is below it: the migrator refuses
// it, and kit gives the next generation again. Every statement of a table's
// migration does nothing when its table exists, so a table created twice —
// its registry row lost — is only a second row in kit_migrations.

const (
	// setKit is kit's own set.
	setKit = "kit"
	// tableKit is the version table of kit's own set.
	tableKit = "kit_migrations"
	// kitTablesTable is kit's registry of its tables.
	kitTablesTable = "kit_tables"
	// registryVersion is the registry's migration, first of kit's set.
	registryVersion uint64 = 1
	// kitSetAttempts bounds how often kit gives new tables the next
	// generation, while other processes apply theirs.
	kitSetAttempts int = 5
)

// tableRecord is one table in kit's registry: the version its migration was
// applied under, and what its index rows were filed with.
type tableRecord struct {
	Table   string `json:"table"`
	Version uint64 `json:"version"`
	Indexes string `json:"indexes,omitempty"`
}

// key is the table's name, the registry's key.
func (t tableRecord) key() string { return t.Table }

// tableRegistry is kit's registry on one database: its store, and what it
// held when the database started.
type tableRegistry struct {
	store *docstore.SQLStore[tableRecord]

	mu    sync.Mutex
	known map[string]tableRecord
}

// get is the registry's record of table.
func (g *tableRegistry) get(table string) (tableRecord, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rec, ok := g.known[table]
	return rec, ok
}

// put records rec.
func (g *tableRegistry) put(ctx context.Context, rec tableRecord) error {
	if err := g.store.Put(ctx, rec); err != nil {
		return err
	}
	g.mu.Lock()
	g.known[rec.Table] = rec
	g.mu.Unlock()
	return nil
}

// kitSet is kit's own set on one database, as this build resolves it: every
// migration, and the tables it gives a version for the first time.
type kitSet struct {
	set   migrationSet
	fresh []tableRecord
}

// registryMigration is the registry's migration.
func registryMigration(dialect sql.Dialect) (sql.Migration, error) {
	return docstore.SQLMigration(dialect, kitTablesTable, registryVersion)
}

// openRegistry reads kit's registry on the database cfg opens; an empty one
// when its table is not there yet — when apply is not set, and its migration
// is pending.
func openRegistry(ctx context.Context, cfg sql.Config, tm sql.Transactor, apply bool) (*tableRegistry, error) {
	first, err := registryMigration(cfg.Dialect)
	if err != nil {
		return nil, err
	}
	m, err := sql.NewMigrator(cfg, sql.MigrateConfig{Migrations: []sql.Migration{first}, VersionTable: tableKit})
	if err != nil {
		return nil, err
	}
	store, err := docstore.OpenSQL(docstore.SQLConfig[tableRecord]{Key: tableRecord.key, Transactor: tm, Dialect: cfg.Dialect, Table: kitTablesTable})
	if err != nil {
		return nil, err
	}
	g := &tableRegistry{store: store, known: map[string]tableRecord{}}
	if apply {
		if err := m.Up(ctx); err != nil {
			return nil, err
		}
	} else if pending, err := m.Plan(ctx); err != nil || len(pending) > 0 {
		return g, err
	}
	all, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, rec := range all {
		g.known[rec.Table] = rec
	}
	return g, nil
}

// resolveKitSet is kit's set on d: the registry's migration, each table the
// registry knows at its version, and each new one at gen, the generation
// given.
func (a *App) resolveKitSet(d *database, g *tableRegistry, gen uint64) (kitSet, error) {
	first, err := registryMigration(d.dialect())
	if err != nil {
		return kitSet{}, err
	}
	out := kitSet{set: migrationSet{name: setKit, table: tableKit, migrations: []sql.Migration{first}}}
	used := map[uint64]bool{registryVersion: true}
	known, fresh, versionsOf := a.kitTablesOf(d, g)
	for _, rec := range known {
		used[rec.Version] = true
		m, err := tableMigration(d.dialect(), rec.Table, versionsOf[rec.Table], rec.Version)
		if err != nil {
			return kitSet{}, err
		}
		out.set.migrations = append(out.set.migrations, m)
	}
	for _, name := range fresh {
		v := freeVersion(used, gen<<32|uint64(nameDigest(name)))
		m, err := tableMigration(d.dialect(), name, versionsOf[name], v)
		if err != nil {
			return kitSet{}, err
		}
		out.set.migrations = append(out.set.migrations, m)
		out.fresh = append(out.fresh, tableRecord{Table: name, Version: v})
	}
	return out, nil
}

// kitTablesOf are kit's tables on d: those g records, with their record,
// and the new ones, sorted; and, for a table of a store's versions, the
// store's table it belongs to.
func (a *App) kitTablesOf(d *database, g *tableRegistry) (known []tableRecord, fresh []string, versionsOf map[string]string) {
	versionsOf = map[string]string{}
	for _, t := range a.kitTables(d) {
		if t.versionsOf != "" {
			versionsOf[t.name] = t.versionsOf
		}
		if rec, ok := g.get(t.name); ok {
			known = append(known, rec)
			continue
		}
		if !slices.Contains(fresh, t.name) {
			fresh = append(fresh, t.name)
		}
	}
	slices.Sort(fresh)
	return known, fresh, versionsOf
}

// freeVersion is v, or the first version above it used does not hold,
// marked used.
func freeVersion(used map[uint64]bool, v uint64) uint64 {
	for used[v] {
		v++
	}
	used[v] = true
	return v
}

// tableMigration is the migration of one of kit's tables: the SDK's
// document store's two tables, or — for a store's versions — the table of
// versions beside the store's, which the SDK names from it.
func tableMigration(dialect sql.Dialect, name, versionsOf string, version uint64) (sql.Migration, error) {
	if versionsOf != "" {
		return docstore.SQLVersionsMigration(dialect, versionsOf, version)
	}
	return docstore.SQLMigration(dialect, name, version)
}

// nameDigest is the low half of a new table's version.
func nameDigest(name string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return h.Sum32()
}

// nextGeneration is the generation of kit's next new tables: above every
// version the registry records.
func nextGeneration(g *tableRegistry) uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	gen := uint64(1)
	for _, rec := range g.known {
		gen = max(gen, rec.Version>>32+1)
	}
	return gen
}

// applyKitSet applies kit's set on d — its registry first, its new tables
// at the next generation, the one after when another process applied a
// newer one meanwhile — and records the new tables. It returns the set as
// it applied it.
func (a *App) applyKitSet(ctx context.Context, d *database, cfg sql.Config, g *tableRegistry) (migrationSet, error) {
	gen := nextGeneration(g)
	var last error
	for range kitSetAttempts {
		set, next, err := a.applyKitSetAt(ctx, d, cfg, g, gen)
		if next == 0 {
			return set, err
		}
		gen, last = next, err
	}
	return migrationSet{}, last
}

// applyKitSetAt applies kit's set on d with its new tables at generation
// gen, and records them. next is the generation to try again at when
// another process applied a newer one meanwhile, zero otherwise.
func (a *App) applyKitSetAt(ctx context.Context, d *database, cfg sql.Config, g *tableRegistry, gen uint64) (set migrationSet, next uint64, err error) {
	ks, err := a.resolveKitSet(d, g, gen)
	if err != nil {
		return migrationSet{}, 0, err
	}
	m, err := sql.NewMigrator(cfg, sql.MigrateConfig{Migrations: ks.set.migrations, VersionTable: tableKit})
	if err != nil {
		return ks.set, 0, err
	}
	if err := m.Up(ctx); err != nil {
		return ks.set, laterGeneration(gen, err), err
	}
	for _, rec := range ks.fresh {
		if err := g.put(ctx, rec); err != nil {
			return ks.set, 0, err
		}
	}
	return ks.set, 0, nil
}

// laterGeneration is the generation above the one another process applied,
// when err says the migrator refused kit's set for it; zero for any other
// refusal.
func laterGeneration(gen uint64, err error) uint64 {
	if !errors.Is(err, sql.MigrationOutOfOrder) {
		return 0
	}
	applied, perr := strconv.ParseUint(fieldOf(err, "applied"), 10, 64)
	if perr != nil {
		return 0
	}
	return max(gen+1, applied>>32+1)
}

// planKitSet is kit's set on d as `migrate status` shows it and a manual
// start judges it: what is applied, and what is pending, applying nothing.
func (a *App) planKitSet(d *database, g *tableRegistry) (migrationSet, error) {
	ks, err := a.resolveKitSet(d, g, nextGeneration(g))
	return ks.set, err
}

// knownTables are the tables kit's registry records, sorted.
func (g *tableRegistry) knownTables() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Sorted(maps.Keys(g.known))
}

// reindex files table's index rows again when what they were filed with —
// fingerprint — is not what the registry records, and records it.
func (r *databaseRun) reindex(ctx context.Context, table, fingerprint string, reindex func(context.Context) error) error {
	r.mu.Lock()
	g := r.registry
	r.mu.Unlock()
	if g == nil {
		return nil
	}
	rec, ok := g.get(table)
	if !ok {
		rec = tableRecord{Table: table}
	}
	if rec.Indexes == fingerprint {
		return nil
	}
	if err := reindex(ctx); err != nil {
		return err
	}
	rec.Indexes = fingerprint
	if !ok {
		// A table kit's set did not give a version to: nothing to record
		// but what was filed.
		return nil
	}
	return g.put(ctx, rec)
}
