package streamengine

import (
	"context"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/internal/app/storeengine"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	sdkstream "github.com/kitsunium/sdk/pkg/v1/data/stream"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// Enable plugs the streams, their consumer groups and the migration of a
// stream's tables into the framework.
func Enable() {
	plug.Streams.Store(&plug.StreamDriverConfig{Open: open, TableMigration: tableMigration})
}

// open opens the stream cfg places: the SDK's SQL stream on its database,
// else a file stream in its directory, else a memory stream; behind a test's
// wrapper when it set one.
//
// IFACE-PLUGIN: kit reaches a stream through plug.Stream; this is its
// implementation.
func open(cfg plug.StreamConfig) (plug.Stream, error) {
	clk := clockOf(cfg.Clock)
	s, err := openStream(&cfg, clk)
	if err != nil {
		return nil, err
	}
	if wrap, ok := cfg.Wrap.(func(sdkstream.Stream) sdkstream.Stream); ok && wrap != nil {
		s = wrap(s)
	}
	return &stream{s: s, clock: clk, name: cfg.Name}, nil
}

// openStream is the SDK's stream cfg places.
func openStream(cfg *plug.StreamConfig, clk clock.Timed) (sdkstream.Stream, error) {
	policy := sdkstream.Policy{Partitions: cfg.Partitions}
	switch {
	case cfg.Database != nil:
		return openSQL(cfg, policy, clk)
	case cfg.Dir != "":
		return sdkstream.NewFile(sdkstream.FileConfig{Clock: clk, Dir: cfg.Dir, Policy: policy})
	}
	return sdkstream.NewMemory(sdkstream.MemoryConfig{Clock: clk, Policy: policy})
}

// openSQL is the SDK's SQL stream over cfg's tables: on the transaction
// manager and in the dialect of the database the store subsystem opened, so
// that an append joins the transaction its context carries.
func openSQL(cfg *plug.StreamConfig, policy sdkstream.Policy, clk clock.Timed) (sdkstream.Stream, error) {
	tm, dialect, ok := storeengine.SQLOf(cfg.Database)
	if !ok {
		return nil, errs.Wrap(sdkstream.SQLStreamMisconfigured, errs.WrapParams{},
			errs.String("stream", cfg.Name), errs.String("problem", "the stream's database was not opened by the store subsystem"))
	}
	sqlCfg := sdkstream.SQLConfig{Table: cfg.Table, Policy: policy, Transactor: tm, Dialect: dialect}
	if clk != nil {
		sqlCfg.Clock = clk
	}
	return sdkstream.NewSQL(sqlCfg)
}

// tableMigration is the migration of a streamed topic's tables on a database
// of the dialect named: the SDK's SQL stream's three tables, named from
// table.
func tableMigration(dialect, table string, version uint64) (plug.MigrationValue, error) {
	d, err := dialectOf(dialect)
	if err != nil {
		return plug.MigrationValue{}, err
	}
	m, err := sdkstream.SQLMigration(d, table, version)
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

// pluggedStep is the SDK's migration step s on the standard library's
// executor, nil for none.
func pluggedStep(s sql.Step) plug.Step {
	if s == nil {
		return nil
	}
	return func(ctx context.Context, ex plug.Executor) error { return s(ctx, ex) }
}
