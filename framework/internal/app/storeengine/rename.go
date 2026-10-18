package storeengine

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// The framework's tables at their present names (ADR 0180 step 5, Q5). A
// release before kept them under kit_*: its version table kit_migrations,
// its registry kit_tables, a consumer's queue kit_queue__…, the privacy
// service's stores kit_privacy__…. The core names each table both ways
// (plug.RenameSpec); this file moves them, and nothing else: no row of a
// moved table is read or written, so what they hold — sealed or not — is
// byte for byte what the former release wrote.
//
// The move is a migration of the former version table: the SDK's migrator
// holds that table's lock — the one a former release takes, so that neither
// runs beside the other — and records the move in the transaction that
// makes it. In it, the history of the former table is copied into the
// present one, which the SDK's migrator made first; each table and the
// tables the document store derives from it are renamed; on PostgreSQL the
// indexes named after a table follow it; and the history's names of the
// tables' migrations are the present ones. Once recorded, the former table
// is dropped: its history is the present one's, and its last row was the
// move's.
//
// Every statement leaves the move one step nearer its end, so a run that
// stopped — MySQL commits a renaming on its own — completes when it runs
// again: a table already under its present name is not moved again, a
// version already copied is not copied twice. What is never done is
// replacing a table: when a former name and its present one both exist,
// the move is refused, every such table named, and nothing moves.

// derivedMark separates a document store's table from the tables the SDK
// derives from it — <table>___ix, <table>___vs —: three underscores no
// table name the framework gives holds.
const derivedMark = "___"

// The phases a refusal of the move names.
const (
	phaseCatalogue = "catalogue"
	phaseHistory   = "history"
	phaseRename    = "rename"
	phaseIndexes   = "indexes"
	phaseLabels    = "labels"
	phaseDrop      = "drop"
)

// formerTables are the tables of plan still under a former name on db, its
// former version table among them, sorted.
func formerTables(ctx context.Context, db plug.Database, plan plug.RenameSpec) ([]string, error) {
	scope, err := renaming(db)
	if err != nil {
		return nil, err
	}
	cat, err := catalogue(ctx, scope.cfg.DB, scope.dialect)
	if err != nil {
		return nil, err
	}
	return stillFormer(cat, plan), nil
}

// renameTables moves the tables of plan to their present names on db, and
// returns those it moved.
func renameTables(ctx context.Context, db plug.Database, plan plug.RenameSpec) ([]string, error) {
	scope, err := renaming(db)
	if err != nil {
		return nil, err
	}
	cat, err := catalogue(ctx, scope.cfg.DB, scope.dialect)
	if err != nil || len(stillFormer(cat, plan)) == 0 {
		return nil, err
	}
	// Judged first on the pool, so that a refusal leaves nothing behind —
	// not even the present version table —, and again under the lock.
	if err := refuseTaken(cat, pairs(cat, plan.Tables)); err != nil {
		return nil, err
	}
	moved, err := scope.runMove(ctx, plan)
	if err != nil {
		return nil, err
	}
	// Its history is the present table's, its last row the move's. A drop
	// that fails refuses this start; the next one records nothing more, and
	// drops it.
	if _, err := scope.cfg.DB.ExecContext(ctx, "DROP TABLE IF EXISTS "+scope.dialect.QuoteIdent(plan.FormerVersions)); err != nil {
		return nil, failedAt(phaseDrop, err)
	}
	return moved, nil
}

// runMove makes the present version table, as the SDK's migrator makes it —
// the history is copied into the table its runs read —, then runs the move
// as a migration of the former one, and returns the tables it moved.
func (d *database) runMove(ctx context.Context, plan plug.RenameSpec) ([]string, error) {
	present, err := sql.NewMigrator(d.cfg, sql.MigrateConfig{VersionTable: plan.Versions})
	if err == nil {
		_, err = present.Plan(ctx)
	}
	if err != nil {
		return nil, said(err)
	}
	var moved []string
	step := func(ctx context.Context, ex sql.Executor) error {
		var err error
		moved, err = move(ctx, ex, d.dialect, plan)
		return err
	}
	m, err := sql.NewMigrator(d.cfg, sql.MigrateConfig{
		VersionTable: plan.FormerVersions,
		Migrations:   []sql.Migration{{Version: plan.Version, Name: plan.Name, Up: step, Down: sql.Irreversible}},
	})
	if err == nil {
		err = m.Up(ctx)
	}
	return moved, said(err)
}

// renaming is db as this package opened it.
func renaming(db plug.Database) (*database, error) {
	scope, ok := db.(*database)
	if !ok {
		return nil, plug.Refuse(plug.RefusalLoad, errs.Wrap(docstore.LoadFailed, errs.WrapParams{},
			errs.String("problem", "the database was not opened by the store subsystem")))
	}
	return scope, nil
}

// catalogueQueries list the tables of the schema a connection works in.
var catalogueQueries = map[sql.Dialect]string{
	sql.DialectPostgres: "SELECT tablename FROM pg_catalog.pg_tables WHERE schemaname = current_schema()",
	sql.DialectMySQL:    "SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE'",
	sql.DialectSQLite:   "SELECT name FROM sqlite_master WHERE type = 'table'",
}

// catalogue is every table of the schema q works in.
func catalogue(ctx context.Context, q sql.Executor, dialect sql.Dialect) (map[string]bool, error) {
	return names(ctx, q, catalogueQueries[dialect], phaseCatalogue)
}

// names is the one column query lists, as a set.
func names(ctx context.Context, q sql.Executor, query, phase string, args ...any) (_ map[string]bool, err error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, failedAt(phase, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = failedAt(phase, cerr)
		}
	}()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, failedAt(phase, err)
		}
		out[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, failedAt(phase, err)
	}
	return out, nil
}

// failedAt is a statement of the move the database did not complete.
func failedAt(phase string, cause error) error {
	return plug.Refuse(plug.RefusalStatement, errors.Join(
		errs.Wrap(docstore.StatementFailed, errs.WrapParams{}, errs.String("phase", phase)), cause))
}

// stillFormer are the tables of plan cat still holds under a former name —
// each table, the tables derived from it, the former version table —,
// sorted.
func stillFormer(cat map[string]bool, plan plug.RenameSpec) []string {
	var out []string
	if cat[plan.FormerVersions] {
		out = append(out, plan.FormerVersions)
	}
	for _, p := range pairs(cat, plan.Tables) {
		out = append(out, p.From)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// pairs are the renamings tables make in cat: each table cat holds under
// its former name, and every table derived from it, each to its present
// name.
func pairs(cat map[string]bool, tables []plug.RenameValue) []plug.RenameValue {
	var out []plug.RenameValue
	for _, t := range tables {
		if t.From == "" || t.From == t.To {
			continue
		}
		if cat[t.From] {
			out = append(out, t)
		}
		for name := range cat {
			if derived, ok := strings.CutPrefix(name, t.From+derivedMark); ok {
				out = append(out, plug.RenameValue{From: name, To: t.To + derivedMark + derived})
			}
		}
	}
	slices.SortFunc(out, func(x, y plug.RenameValue) int { return strings.Compare(x.From, y.From) })
	return slices.CompactFunc(out, func(x, y plug.RenameValue) bool { return x.From == y.From })
}

// move is the step of the move's migration, on its transaction: what is
// refused first, then the history, the tables, their indexes and the
// history's names. It returns the tables it renamed.
func move(ctx context.Context, ex sql.Executor, dialect sql.Dialect, plan plug.RenameSpec) ([]string, error) {
	cat, err := catalogue(ctx, ex, dialect)
	if err != nil {
		return nil, err
	}
	renames := pairs(cat, plan.Tables)
	if err := refuseTaken(cat, renames); err != nil {
		return nil, err
	}
	if err := copyHistory(ctx, ex, dialect, plan, cat[plan.FormerVersions]); err != nil {
		return nil, err
	}
	if err := renameAll(ctx, ex, dialect, renames); err != nil {
		return nil, err
	}
	if err := renameIndexes(ctx, ex, dialect, renames); err != nil {
		return nil, err
	}
	if err := relabel(ctx, ex, dialect, plan); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(renames))
	for _, p := range renames {
		out = append(out, p.From)
	}
	return out, nil
}

// refuseTaken is the refusal of renames whose present names cat holds
// too, every one named; nil when none is.
func refuseTaken(cat map[string]bool, renames []plug.RenameValue) error {
	var taken []string
	for _, p := range renames {
		if cat[p.To] {
			taken = append(taken, p.From+" -> "+p.To)
		}
	}
	if len(taken) == 0 {
		return nil
	}
	return plug.Refuse(plug.RefusalRenameTarget, errs.Wrap(docstore.StatementFailed, errs.WrapParams{},
		errs.String("tables", strings.Join(taken, ", "))))
}

// relabel gives the tables' migrations in the history the names this
// release says.
func relabel(ctx context.Context, ex sql.Executor, dialect sql.Dialect, plan plug.RenameSpec) error {
	for _, l := range plan.Labels {
		_, err := ex.ExecContext(ctx, "UPDATE "+dialect.QuoteIdent(plan.Versions)+" SET name = "+dialect.Placeholder(1)+
			" WHERE name = "+dialect.Placeholder(2), l.To, l.From)
		if err != nil {
			return failedAt(phaseLabels, err)
		}
	}
	return nil
}

// copyHistory copies into the present version table every version the
// former one records and it does not — the move's own excepted, which the
// former table records last —; nothing when found says the former table is
// gone.
func copyHistory(ctx context.Context, ex sql.Executor, dialect sql.Dialect, plan plug.RenameSpec, found bool) error {
	if !found {
		return nil
	}
	former, present := dialect.QuoteIdent(plan.FormerVersions), dialect.QuoteIdent(plan.Versions)
	_, err := ex.ExecContext(ctx, "INSERT INTO "+present+" (version, name, applied_at_unix)"+
		" SELECT f.version, f.name, f.applied_at_unix FROM "+former+" f WHERE f.version <> "+dialect.Placeholder(1)+
		" AND NOT EXISTS (SELECT 1 FROM "+present+" p WHERE p.version = f.version)", int64(plan.Version))
	if err != nil {
		return failedAt(phaseHistory, err)
	}
	return nil
}

// renameAll renames every table of renames: on MySQL in one statement,
// which MySQL runs whole or not at all; one statement per table elsewhere,
// in the move's transaction.
func renameAll(ctx context.Context, ex sql.Executor, dialect sql.Dialect, renames []plug.RenameValue) error {
	if len(renames) == 0 {
		return nil
	}
	var stmts []string
	if dialect == sql.DialectMySQL {
		parts := make([]string, 0, len(renames))
		for _, p := range renames {
			parts = append(parts, dialect.QuoteIdent(p.From)+" TO "+dialect.QuoteIdent(p.To))
		}
		stmts = []string{"RENAME TABLE " + strings.Join(parts, ", ")}
	} else {
		for _, p := range renames {
			stmts = append(stmts, "ALTER TABLE "+dialect.QuoteIdent(p.From)+" RENAME TO "+dialect.QuoteIdent(p.To))
		}
	}
	for _, s := range stmts {
		if _, err := ex.ExecContext(ctx, s); err != nil {
			return failedAt(phaseRename, err)
		}
	}
	return nil
}

// renameIndexes renames, on PostgreSQL, the indexes named after a renamed
// table — a primary key's, a unique constraint's, which PostgreSQL names
// <table>_… and keeps when the table is renamed — so that they are named
// after its present name; renaming an index renames its constraint.
// SQLite renames its own with the table, and MySQL names them after their
// columns.
func renameIndexes(ctx context.Context, ex sql.Executor, dialect sql.Dialect, renames []plug.RenameValue) error {
	if dialect != sql.DialectPostgres {
		return nil
	}
	quote := dialect.QuoteIdent
	for _, p := range renames {
		indexes, err := names(ctx, ex, "SELECT indexname FROM pg_catalog.pg_indexes WHERE schemaname = current_schema() AND tablename = $1", phaseIndexes, p.To)
		if err != nil {
			return err
		}
		for _, idx := range slices.Sorted(maps.Keys(indexes)) {
			to, ok := indexAfter(idx, p)
			if !ok {
				continue
			}
			if _, err := ex.ExecContext(ctx, "ALTER INDEX "+quote(idx)+" RENAME TO "+quote(to)); err != nil {
				return failedAt(phaseIndexes, err)
			}
		}
	}
	return nil
}

// indexAfter is the name of the index idx once its table p is renamed: its
// former table's name, as much of it as PostgreSQL kept, replaced by as
// much of the present one; false when idx is not named after it, or the
// two names differ in length — the framework's never do.
func indexAfter(idx string, p plug.RenameValue) (string, bool) {
	if len(p.From) != len(p.To) {
		return "", false
	}
	n := min(len(idx), len(p.From))
	if n == 0 || idx[:n] != p.From[:n] {
		return "", false
	}
	return p.To[:n] + idx[n:], true
}
