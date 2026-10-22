package queueengine

import (
	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/internal/app/storeengine"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// Enable plugs the queues' brokers, their consumer and the migration of a
// queue's table into the framework.
func Enable() {
	plug.Queues.Store(&plug.QueueDriverConfig{
		Open: open, TableMigration: tableMigration, TrailMigration: trailMigration,
		DoNotRetry: queue.DoNotRetry, NotRetryable: notRetryable,
	})
}

// open opens the queue cfg places: one table of the SDK's SQL broker on its
// database, else a file queue in its directory, else a memory queue; a
// mailer's outbox behind its sealing, and behind a test's wrapper when it
// set one.
//
// IFACE-PLUGIN: kit reaches a queue through plug.Broker; this is its
// implementation.
func open(cfg plug.QueueConfig) (plug.Broker, error) {
	policy := policyOf(cfg.Policy)
	clk := clockOf(cfg.Clock)
	var (
		b   queue.Broker
		err error
	)
	switch {
	case cfg.Database != nil:
		b, err = openSQL(&cfg, policy, clk)
	case cfg.Dir != "":
		b, err = queue.NewFile(queue.FileConfig{Clock: clk, Dir: cfg.Dir, Policy: policy})
	default:
		b, err = queue.NewMemory(queue.MemoryConfig{Clock: clk, Policy: policy})
	}
	if err != nil {
		return nil, err
	}
	if cfg.Seal != nil {
		if b, err = newSealedOutbox(b, &cfg); err != nil {
			return nil, err
		}
	}
	if wrap, ok := cfg.Wrap.(func(queue.Broker) queue.Broker); ok && wrap != nil {
		b = wrap(b)
	}
	return &broker{q: b, clock: clk}, nil
}

// openSQL is the SDK's SQL broker over cfg's table: on the transaction
// manager and in the dialect of the database the store subsystem opened,
// so that a publication joins the transaction its context carries.
func openSQL(cfg *plug.QueueConfig, policy queue.Policy, clk clock.Timed) (queue.Broker, error) {
	tm, dialect, ok := storeengine.SQLOf(cfg.Database)
	if !ok {
		return nil, errs.Wrap(queue.SQLQueueMisconfigured, errs.WrapParams{},
			errs.String("queue", cfg.Name), errs.String("problem", "the queue's database was not opened by the store subsystem"))
	}
	// Trail: the framework's queue tables have the column, which
	// trailMigration adds to those an earlier release made (issue #500).
	return queue.NewSQL(queue.SQLConfig{Table: cfg.Table, Policy: policy, Clock: clk, Transactor: tm, Dialect: dialect, Trail: true})
}

// policyOf is kit's policy as the SDK's queue takes it.
func policyOf(p plug.QueuePolicyValue) queue.Policy {
	return queue.Policy{
		VisibilityTimeout: p.VisibilityTimeout, RetryDelay: p.RetryDelay, MaxRetryDelay: p.MaxRetryDelay,
		MaxDeliveries: p.MaxDeliveries, MaxMessageBytes: p.MaxMessageBytes,
	}
}

// notRetryable reports whether err says that no retry can help: the SDK's
// mark, which DoNotRetry puts on it.
func notRetryable(err error) bool { return errs.HasCode(err, queue.CodeNotRetryable) }

// tableMigration is the migration of a consumer's queue's table on a
// database of the dialect named: the SDK's SQL broker's one table (ADR 0178
// C1).
func tableMigration(dialect, table string, version uint64) (plug.MigrationValue, error) {
	d, err := dialectOf(dialect)
	if err != nil {
		return plug.MigrationValue{}, err
	}
	m, err := queue.SQLMigration(d, table, version)
	if err != nil {
		return plug.MigrationValue{}, err
	}
	return plug.MigrationValue{Version: m.Version, Name: m.Name, Up: pluggedStep(m.Up), Down: pluggedStep(m.Down)}, nil
}

// trailMigration is the migration that adds the column a consumer's queue's
// dead letters keep their trails in to its table on a database of the dialect
// named — the SDK's SQL broker's trail column, which a table made before it
// existed lacks; nothing on a table that has it (issue #500).
func trailMigration(dialect, table string, version uint64) (plug.MigrationValue, error) {
	d, err := dialectOf(dialect)
	if err != nil {
		return plug.MigrationValue{}, err
	}
	m, err := queue.SQLTrailMigration(d, table, version)
	if err != nil {
		return plug.MigrationValue{}, err
	}
	return plug.MigrationValue{Version: m.Version, Name: m.Name, Up: pluggedStep(m.Up), Down: pluggedStep(m.Down)}, nil
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
