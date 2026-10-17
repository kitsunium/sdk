package storekit

import (
	"context"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// kit's scope on a database (ADR 0179 §2), over the SDK's sql: the
// transaction manager its stores and kit's transactions run on, its check,
// and its migrations. kit's core holds it as a plug.Database and opens
// every transaction, savepoint and held function through it.

// database is one database as the store subsystem runs it.
type database struct {
	cfg     sql.Config
	tm      sql.Transactor
	checker sql.Checker
	dialect sql.Dialect
}

// openDatabase opens kit's scope on the pool cfg names: its tuning applied
// — the SDK refuses a pool policy it cannot honour —, its transaction
// manager and its check built.
func openDatabase(cfg plug.DatabaseConfig) (plug.Database, error) {
	dialect, err := dialectOf(cfg.Dialect)
	if err != nil {
		return nil, err
	}
	scfg := sql.Config{
		DB: cfg.DB, Dialect: dialect,
		Pool: sql.PoolConfig{MaxOpen: cfg.MaxOpen, MaxIdle: cfg.MaxIdle, MaxLifetime: cfg.MaxLifetime, MaxIdleTime: cfg.MaxIdleTime},
		// A database's budgets are the network's, in wall time: a test's
		// manual clock would hold a check or a migration lock forever.
		Clock:        clock.System,
		CheckTimeout: cfg.CheckTimeout,
	}
	checker, err := sql.NewChecker(scfg)
	if err != nil {
		return nil, said(err)
	}
	tm, err := sql.NewTransactor(scfg)
	if err != nil {
		return nil, said(err)
	}
	return &database{cfg: scfg, tm: tm, checker: checker, dialect: dialect}, nil
}

// dialectOf is the SDK's dialect the engine's name says.
func dialectOf(name string) (sql.Dialect, error) {
	switch name {
	case model.EnginePostgres:
		return sql.DialectPostgres, nil
	case model.EngineMySQL:
		return sql.DialectMySQL, nil
	case model.EngineSQLite:
		return sql.DialectSQLite, nil
	}
	return 0, errs.Wrap(sql.UnknownDialect, errs.WrapParams{}, errs.String("dialect", name))
}

// Transact runs fn in a transaction of the database, or in a savepoint of
// the one ctx carries for it.
func (d *database) Transact(ctx context.Context, fn func(ctx context.Context) error) error {
	return said(d.tm.Transact(ctx, sql.TxOptions{}, func(ctx context.Context, _ sql.Executor) error {
		return fn(ctx)
	}))
}

// InTransaction reports whether ctx carries a transaction of the database.
func (d *database) InTransaction(ctx context.Context) bool {
	j, ok := d.tm.(sql.Joiner)
	if !ok {
		return false
	}
	_, in := j.Join(ctx)
	return in
}

// Defer holds fn until the transaction ctx carries for the database
// commits.
func (d *database) Defer(ctx context.Context, fn func()) bool {
	df, ok := d.tm.(sql.Deferrer)
	return ok && df.Defer(ctx, fn)
}

// OnAbort holds fn until the work of the scope ctx carries for the database
// is undone.
func (d *database) OnAbort(ctx context.Context, fn func(ctx context.Context)) bool {
	ab, ok := d.tm.(sql.Aborter)
	return ok && ab.OnAbort(ctx, fn)
}

// Check asks the database whether it answers.
func (d *database) Check(ctx context.Context) error { return said(d.checker.Check(ctx)) }

// Migrator is the set ms on the database, under the version table table.
func (d *database) Migrator(table string, ms []plug.MigrationValue) (plug.Migrator, error) {
	m, err := sql.NewMigrator(d.cfg, sql.MigrateConfig{Migrations: sqlMigrations(ms), VersionTable: table})
	if err != nil {
		return nil, said(err)
	}
	return migrator{m: m}, nil
}

// Handle sets what dst points at to the SDK's handle of its type: the
// transaction manager, the transaction manager as a Deferrer that is an
// Aborter too — what a mailer's spool takes —, the dialect.
func (d *database) Handle(dst any) bool {
	switch p := dst.(type) {
	case *sql.Transactor:
		*p = d.tm
	case *sql.Deferrer:
		df, ok := d.tm.(sql.Deferrer)
		if _, aborts := d.tm.(sql.Aborter); !ok || !aborts {
			return false
		}
		*p = df
	case *sql.Dialect:
		*p = d.dialect
	default:
		return false
	}
	return true
}

// migrator is one set of migrations under the SDK's Migrator.
type migrator struct {
	m sql.Migrator
}

// Plan is the set's pending migrations.
func (m migrator) Plan(ctx context.Context) ([]plug.MigrationValue, error) {
	pending, err := m.m.Plan(ctx)
	return pluggedMigrations(pending), said(err)
}

// Up applies every pending migration.
func (m migrator) Up(ctx context.Context) error { return said(m.m.Up(ctx)) }

// Down reverses the applied migrations above version.
func (m migrator) Down(ctx context.Context, version uint64) error {
	return said(m.m.Down(ctx, version))
}

// sqlMigrations are kit's migrations as the SDK's migrator runs them.
func sqlMigrations(ms []plug.MigrationValue) []sql.Migration {
	out := make([]sql.Migration, len(ms))
	for i, m := range ms {
		out[i] = sql.Migration{Version: m.Version, Name: m.Name, Up: sqlStep(m.Up), Down: sqlStep(m.Down)}
	}
	return out
}

// sqlStep is s as the SDK's migrator runs it, nil for none: the SDK's
// executor is the standard library's, which s takes as it is.
func sqlStep(s plug.Step) sql.Step {
	if s == nil {
		return nil
	}
	return func(ctx context.Context, ex sql.Executor) error { return s(ctx, ex) }
}

// pluggedMigrations are the SDK's migrations as kit's core reads them.
func pluggedMigrations(ms []sql.Migration) []plug.MigrationValue {
	if ms == nil {
		return nil
	}
	out := make([]plug.MigrationValue, len(ms))
	for i, m := range ms {
		out[i] = pluggedMigration(m)
	}
	return out
}

// pluggedMigration is the SDK's migration m as kit's core reads it.
func pluggedMigration(m sql.Migration) plug.MigrationValue {
	return plug.MigrationValue{Version: m.Version, Name: m.Name, Up: pluggedStep(m.Up), Down: pluggedStep(m.Down)}
}

// pluggedStep is the SDK's step s on the standard library's executor, nil
// for none.
func pluggedStep(s sql.Step) plug.Step {
	if s == nil {
		return nil
	}
	return func(ctx context.Context, ex plug.Executor) error { return s(ctx, ex) }
}

// tableMigration is the migration of one of kit's tables: the SDK's
// document store's two tables; for a store's versions, the table of
// versions beside the store's, which the SDK names from it; for a
// consumer's queue, the SQL broker's one table (ADR 0178 C1) — until the
// queues leave kit's core (ADR 0179, step 4).
func tableMigration(dialect string, t plug.TableSpec, version uint64) (plug.MigrationValue, error) {
	d, err := dialectOf(dialect)
	if err != nil {
		return plug.MigrationValue{}, err
	}
	var m sql.Migration
	switch {
	case t.Queue:
		m, err = queue.SQLMigration(d, t.Name, version)
	case t.VersionsOf != "":
		m, err = docstore.SQLVersionsMigration(d, t.VersionsOf, version)
	default:
		m, err = docstore.SQLMigration(d, t.Name, version)
	}
	if err != nil {
		return plug.MigrationValue{}, said(err)
	}
	return pluggedMigration(m), nil
}
