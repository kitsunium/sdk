// Package kit — the migrate command.
package kit

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// The migrate command. A database's migrations come in sets, one per origin
// — kit's own, the product's (kit.Migrations), a module's —, each with its
// own version table and so its own lock; they run when the database starts
// (<name>-migrate: start), or by the product's binary, in the environment it
// runs with: `<product> migrate`, or `kit migrate` in dev. Doctrine's
// doctrine:migrations:status and :migrate, which Symfony runs on deploy.
//
//	migrate [status]            every set: what its version table records, what is pending
//	migrate up                  every pending migration, kit's set first
//	migrate down SET VERSION    SET's migrations above VERSION, reversed newest first
//
// SET is a set's origin — product —, or database/origin when several
// databases have one by that name.

// migrateCommand runs `migrate status|up|down SET VERSION`.
func (a *App) migrateCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	sub, rest, ok := migrateArgs(args)
	if !ok {
		fmt.Fprintf(stderr, "usage: %[1]s migrate [status] | %[1]s migrate up | %[1]s migrate down SET VERSION\n", a.name)
		return 2
	}
	if err := a.migrateReady(ctx); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer a.closeSecrets()
	if a.migrateRefused(stderr) {
		return 1
	}
	run := &migrateRun{a: a, sub: sub, tw: tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0), stderr: stderr}
	if sub == "down" {
		if err := run.aim(rest[0], rest[1]); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	return run.all(ctx)
}

// migrateArgs reads the command's arguments: its subcommand, status by
// default, and what follows it.
func migrateArgs(args []string) (sub string, rest []string, ok bool) {
	sub = "status"
	if len(args) > 0 {
		sub, rest = args[0], args[1:]
	}
	switch {
	case sub == "status" && len(rest) == 0, sub == "up" && len(rest) == 0, sub == "down" && len(rest) == 2:
		return sub, rest, true
	}
	return sub, rest, false
}

// migrateReady resolves the app and opens the environment's secret stores,
// where the databases' URLs are: a product with no database, or kept in
// memory, has nothing to migrate.
func (a *App) migrateReady(ctx context.Context) error {
	if err := a.resolve(); err != nil {
		return err
	}
	switch {
	case len(a.opts.databases) == 0:
		return failure(CodeMigrateRefused, "MIGRATE_REFUSED", "the product declares no database", nil)
	case a.opts.memory:
		return failure(CodeMigrateRefused, "MIGRATE_REFUSED", "the app keeps its data in memory (kit.InMemory): it opens no database", nil)
	}
	return a.openSecrets(ctx)
}

// migrateRefused resolves the settings — a database's tuning among them —
// and says every error the start would refuse the databases for.
func (a *App) migrateRefused(stderr io.Writer) bool {
	resolved := a.resolveSettings()
	a.settingValues.Store(&resolved.values)
	refused := false
	for _, d := range slices.Concat(resolved.problems, a.databaseProblems()) {
		if d.Severity == "error" {
			fmt.Fprintf(stderr, "error: %s\n", d.Message)
			refused = true
		}
	}
	return refused
}

// migrateRun is one run of the migrate command: what it does, the set down
// aims at, and where it says what happened.
type migrateRun struct {
	a      *App
	sub    string
	tw     *tabwriter.Writer
	stderr io.Writer
	// target and version are down's: the set, and the version it goes back
	// to.
	target  *setRef
	version uint64
}

// aim reads down's arguments: the set, and the version to go back to.
func (r *migrateRun) aim(set, version string) error {
	ref, err := r.a.findSet(set)
	if err != nil {
		return err
	}
	v, err := strconv.ParseUint(version, 10, 64)
	if err != nil {
		return failure(CodeMigrateRefused, "MIGRATE_REFUSED", clip(version)+" is not a version: a migration's version is a number, 20260901120000", nil)
	}
	r.target, r.version = &ref, v
	return nil
}

// all runs the command on every set of every database it concerns, and
// returns its exit status.
func (r *migrateRun) all(ctx context.Context) int {
	if r.sub == "status" {
		fmt.Fprintln(r.tw, "DATABASE\tSET\tTABLE\tVERSION\tNAME\tSTATE")
	}
	status := 0
	for _, d := range r.a.opts.databases {
		if !r.concerns(d) {
			continue
		}
		if !r.database(ctx, d) {
			status = 1
		}
	}
	if err := r.tw.Flush(); err != nil {
		fmt.Fprintln(r.stderr, err)
		return 1
	}
	return status
}

// concerns says whether the command runs on d: a database with sets — kit's
// own, when it keeps stores —, and down's own.
func (r *migrateRun) concerns(d *database) bool {
	return (len(r.a.migrationSets(d)) > 0 || len(r.a.kitTables(d)) > 0) && (r.target == nil || r.target.db == d)
}

// database runs the command on the sets of one database, opened for it, and
// reports whether it went through. A database with no URL in dev has
// nothing to migrate.
func (r *migrateRun) database(ctx context.Context, d *database) bool {
	o, err := r.a.openDatabase(ctx, d)
	switch {
	case err != nil:
		fmt.Fprintf(r.stderr, "%s: %s\n", d.name, errs.PublicOf(err))
		return false
	case o == nil:
		fmt.Fprintf(r.stderr, "%s: no URL in dev (%s): nothing to migrate\n", d.name, r.a.urlVariable(d))
		return true
	}
	defer func() {
		if err := o.pool.Close(); err != nil {
			fmt.Fprintf(r.stderr, "%s: closing the pool: %s\n", d.name, errs.PublicOf(err))
		}
	}()
	ok := r.target != nil || len(r.a.kitTables(d)) == 0 || r.kitSet(ctx, d, o)
	return r.sets(ctx, d, o) && ok
}

// sets runs the command on d's sets — the one it targets, or all — and
// reports whether each went through.
func (r *migrateRun) sets(ctx context.Context, d *database, o *opened) bool {
	ok := true
	for _, set := range r.a.migrationSets(d) {
		if r.target != nil && r.target.set.name != set.name {
			continue
		}
		if !r.set(ctx, d, set, o) {
			ok = false
		}
	}
	return ok
}

// set runs the command on one set, and reports whether it went through.
func (r *migrateRun) set(ctx context.Context, d *database, set migrationSet, o *opened) bool {
	m, err := sql.NewMigrator(o.cfg, sql.MigrateConfig{Migrations: set.migrations, VersionTable: set.table})
	if err == nil {
		switch r.sub {
		case "status":
			err = r.status(ctx, d, set, m)
		case "up":
			err = r.up(ctx, d, set, m)
		case "down":
			err = r.down(ctx, d, set, m)
		}
	}
	if err != nil {
		fmt.Fprintf(r.stderr, "%s/%s: %s\n", d.name, set.name, errs.PublicOf(r.a.migrationFailure(d, set, err)))
		return false
	}
	return true
}

// kitSet runs the command on kit's own set — status, or up —, and reports
// whether it went through: its registry read, its tables at the versions it
// records, a new one at the next generation.
func (r *migrateRun) kitSet(ctx context.Context, d *database, o *opened) bool {
	fail := func(set migrationSet, err error) bool {
		fmt.Fprintf(r.stderr, "%s/%s: %s\n", d.name, setKit, errs.PublicOf(r.a.migrationFailure(d, set, err)))
		return false
	}
	g, err := openRegistry(ctx, o.cfg, o.tm, false)
	if err != nil {
		return fail(migrationSet{name: setKit, table: tableKit}, err)
	}
	set, err := r.a.planKitSet(d, g)
	if err != nil {
		return fail(set, err)
	}
	m, err := sql.NewMigrator(o.cfg, sql.MigrateConfig{Migrations: set.migrations, VersionTable: set.table})
	if err != nil {
		return fail(set, err)
	}
	if r.sub == "status" {
		if err := r.status(ctx, d, set, m); err != nil {
			return fail(set, err)
		}
		return true
	}
	pending, err := m.Plan(ctx)
	if err != nil {
		return fail(set, err)
	}
	if g, err = openRegistry(ctx, o.cfg, o.tm, true); err == nil {
		set, err = r.a.applyKitSet(ctx, d, o.cfg, g)
	}
	switch {
	case err != nil:
		return fail(set, err)
	case len(pending) == 0:
		fmt.Fprintf(r.tw, "%s/%s: up to date\n", d.name, setKit)
	default:
		fmt.Fprintf(r.tw, "%s/%s: applied %s\n", d.name, setKit, versionsText(pending))
	}
	return true
}

// status prints what a set's version table records and what is pending.
func (r *migrateRun) status(ctx context.Context, d *database, set migrationSet, m sql.Migrator) error {
	pending, err := m.Plan(ctx)
	if err != nil {
		return err
	}
	st := setStatus(set, pending)
	for _, m := range st.Applied {
		fmt.Fprintf(r.tw, "%s\t%s\t%s\t%s\t%s\t%s\n", d.name, set.name, set.table, versionText(m.Version), m.Name, "applied")
	}
	for _, m := range st.Pending {
		fmt.Fprintf(r.tw, "%s\t%s\t%s\t%s\t%s\t%s\n", d.name, set.name, set.table, versionText(m.Version), m.Name, "pending")
	}
	if len(st.Applied)+len(st.Pending) == 0 {
		fmt.Fprintf(r.tw, "%s\t%s\t%s\t-\t-\t%s\n", d.name, set.name, set.table, "empty")
	}
	return nil
}

// up applies a set's pending migrations, and says which.
func (r *migrateRun) up(ctx context.Context, d *database, set migrationSet, m sql.Migrator) error {
	pending, err := m.Plan(ctx)
	if err == nil {
		err = m.Up(ctx)
	}
	switch {
	case err != nil:
		return err
	case len(pending) == 0:
		fmt.Fprintf(r.tw, "%s/%s: up to date\n", d.name, set.name)
	default:
		fmt.Fprintf(r.tw, "%s/%s: applied %s\n", d.name, set.name, versionsText(pending))
	}
	return nil
}

// down reverses a set's migrations above the version, and says which,
// newest first — the order Down reverses them in.
func (r *migrateRun) down(ctx context.Context, d *database, set migrationSet, m sql.Migrator) error {
	pending, err := m.Plan(ctx)
	if err == nil {
		err = m.Down(ctx, r.version)
	}
	if err != nil {
		return err
	}
	var reversed []sql.Migration
	for _, m := range slices.Backward(setStatus(set, pending).Applied) {
		if m.Version > r.version {
			reversed = append(reversed, sql.Migration{Version: m.Version, Name: m.Name})
		}
	}
	if len(reversed) == 0 {
		fmt.Fprintf(r.tw, "%s/%s: nothing above %s\n", d.name, set.name, versionText(r.version))
		return nil
	}
	fmt.Fprintf(r.tw, "%s/%s: reversed %s\n", d.name, set.name, versionsText(reversed))
	return nil
}

// setRef is a set the command names, on its database.
type setRef struct {
	db  *database
	set migrationSet
}

// findSet finds the set a command names: its origin — product —, or
// database/origin when several databases have one by that name.
func (a *App) findSet(name string) (setRef, error) {
	dbName, setName, qualified := strings.Cut(name, "/")
	if !qualified {
		dbName, setName = "", name
	}
	if setName == setKit {
		return setRef{}, failure(CodeMigrateRefused, "MIGRATE_REFUSED", "kit's own set is not migrated down: its migrations drop the stores' tables, and every entity in them", nil)
	}
	found, all := a.matchingSets(dbName, setName)
	switch {
	case len(found) == 1:
		return found[0], nil
	case len(found) > 1:
		return setRef{}, failure(CodeMigrateRefused, "MIGRATE_REFUSED", "several databases have a "+setName+" set: write database/"+setName+", one of "+strings.Join(all, ", "), nil)
	case len(all) == 0:
		return setRef{}, failure(CodeMigrateRefused, "MIGRATE_REFUSED", "no database of the product has migrations", nil)
	default:
		return setRef{}, failure(CodeMigrateRefused, "MIGRATE_REFUSED", "no set named "+clip(name)+": the sets are "+strings.Join(all, ", "), nil)
	}
}

// matchingSets are the migration sets named setName — of the database
// dbName, or of any when it is "" — and every set, as database/set.
func (a *App) matchingSets(dbName, setName string) (found []setRef, all []string) {
	for _, d := range a.opts.databases {
		for _, set := range a.migrationSets(d) {
			all = append(all, d.name+"/"+set.name)
			if set.name == setName && (dbName == "" || dbName == d.name) {
				found = append(found, setRef{db: d, set: set})
			}
		}
	}
	return found, all
}

// versionsText lists migrations as a command prints them: their versions,
// and their names.
func versionsText(ms []sql.Migration) string {
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = versionText(m.Version) + " " + strconv.Quote(m.Name)
	}
	return strings.Join(parts, ", ")
}
