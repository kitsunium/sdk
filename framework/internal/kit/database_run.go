// Package kit — a database in a running app.
package kit

import (
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/health"
	"github.com/kitsunium/sdk/pkg/v1/logger"
	"github.com/kitsunium/sdk/pkg/v1/secret"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// A database in a running app: a lifecycle component, database:<name>,
// after the secrets and before the stores. It opens the pool through its
// engine, applies its tuning, runs its migrations and answers its check; a
// database that does not answer fails the start, and the lifecycle unwinds
// what started. Then it is a readiness check — never a liveness one: a
// database outage must not restart every process.
//
// Nothing a driver says leaves this file: its words may name the user, the
// host or the statement. An error, a log line, the graph and the Studio say
// the database, its engine, host:port and name, its TLS mode, and the SDK's
// verdict — a sentence of no infrastructure (ADR 0055 D9 there).

// databaseRun is a database in a running app.
type databaseRun struct {
	d *database

	mu      sync.Mutex
	state   string
	url     DatabaseURLValue
	urlFrom string
	pool    *stdsql.DB
	cfg     sql.Config
	checker sql.Checker
	// ready and checkedAt are its last check's; problem, the public text of
	// its last failure.
	ready     bool
	checkedAt time.Time
	problem   string
	sets      []model.MigrationSet
}

// newDatabaseRuns makes the run of each database the app declares, closed
// — or kept in memory, when the app keeps its data there: nothing opens.
func (a *App) newDatabaseRuns() []*databaseRun {
	runs := make([]*databaseRun, 0, len(a.opts.databases))
	for _, d := range a.opts.databases {
		state := model.DatabaseClosed
		if a.opts.memory {
			state = model.DatabaseMemory
		}
		runs = append(runs, &databaseRun{d: d, state: state})
	}
	a.mu.Lock()
	a.rt.dbs = runs
	a.mu.Unlock()
	return runs
}

// databaseRuns are the current run's databases.
func (a *App) databaseRuns() []*databaseRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.rt.dbs)
}

// addDatabases registers a lifecycle component per database — none when the
// app keeps its data in memory — and a readiness check for each.
func (a *App) addDatabases() error {
	for _, r := range a.newDatabaseRuns() {
		if a.opts.memory || r.d.engine == nil {
			continue
		}
		if err := a.component("database:"+r.d.name, "", func(ctx context.Context) error { return a.startDatabase(ctx, r) },
			func(context.Context) error { return r.close() }); err != nil {
			return err
		}
		budget := resolvedSetting(a, r.d.checkTimeout)
		if err := a.health.AddReadiness(health.ReadinessCheck{
			Name: "database:" + r.d.name, Timeout: budget,
			Check: func(ctx context.Context) error { return a.checkDatabase(ctx, r) },
		}); err != nil {
			return err
		}
	}
	return nil
}

// resolvedSetting is a setting kit declared for itself, as the start
// resolved it: its default before.
func resolvedSetting[T SettingValue](a *App, s *SettingService[T]) T {
	if values := a.settingValues.Load(); values != nil {
		if t, ok := (*values)[s.id].(T); ok {
			return t
		}
	}
	return s.def
}

// opened is a database's pool, open, with what it runs on.
type opened struct {
	url  DatabaseURLValue
	from string
	pool *stdsql.DB
	cfg  sql.Config
}

// openDatabase reads the database's URL where the environment keeps it,
// has its engine describe and open it, and applies its tuning. A database
// without a URL in dev is not opened: nil, and no error.
func (a *App) openDatabase(ctx context.Context, d *database) (*opened, error) {
	url, from, err := a.databaseURL(ctx, d)
	switch {
	case errors.Is(err, secret.NotFound) && a.cfg.env == EnvDev:
		return nil, nil
	case err != nil:
		return nil, a.urlFailure(d, err)
	}
	desc, err := a.describeURL(d, url)
	if err != nil {
		return nil, err
	}
	pool, err := a.openPool(d, url, &desc)
	if err != nil {
		return nil, err
	}
	cfg, err := a.tune(d, pool, &desc)
	if err != nil {
		_ = pool.Close()
		return nil, err
	}
	return &opened{url: desc, from: from, pool: pool, cfg: cfg}, nil
}

// dbFields are a database's error fields: its name, its URL's variable and,
// once described, its engine and where it is.
func (a *App) dbFields(d *database, desc *DatabaseURLValue) []errs.Field {
	fields := []errs.Field{errs.String("database", d.name), errs.String("variable", a.urlVariable(d))}
	if desc != nil {
		fields = append(fields, errs.String("engine", d.engineName()), errs.String("address", desc.location()))
	}
	return fields
}

// urlFailure is a URL set nowhere, or unreadable, in kit's words.
func (a *App) urlFailure(d *database, err error) error {
	if errors.Is(err, secret.NotFound) {
		return dbFailure(CodeDatabaseConfig, "DATABASE_URL_MISSING", fmt.Sprintf("database %q has no URL: set %s", d.name, a.urlVariable(d)), a.dbFields(d, nil)...)
	}
	return dbFailure(CodeDatabaseConfig, "DATABASE_URL_UNREADABLE", fmt.Sprintf("the URL of database %q cannot be read: %s", d.name, errs.PublicOf(err)), a.dbFields(d, nil)...)
}

// describeURL is what d's engine says of its URL — refused when it cannot
// read it, or when it leaves TLS to the driver where kit asks it written.
func (a *App) describeURL(d *database, url secret.Value) (DatabaseURLValue, error) {
	desc, err := d.engine.Describe(url)
	switch {
	case err != nil:
		return desc, dbFailure(CodeDatabaseConfig, "DATABASE_URL_INVALID", fmt.Sprintf("the URL of database %q is not one %s reads: check %s", d.name, engineTitle(d.engineName()), a.urlVariable(d)), a.dbFields(d, nil)...)
	case a.tlsLeft(&desc):
		return desc, dbFailure(CodeDatabaseConfig, "DATABASE_TLS_UNSTATED", fmt.Sprintf("the URL of database %q leaves TLS to the driver: write its mode in %s", d.name, a.urlVariable(d)), a.dbFields(d, nil)...)
	}
	return desc, nil
}

// openPool has d's engine open its pool, which asks for the URL as it is
// now before each new connection.
func (a *App) openPool(d *database, url secret.Value, desc *DatabaseURLValue) (*stdsql.DB, error) {
	current := func(ctx context.Context) (secret.Value, error) {
		v, _, err := a.databaseURL(ctx, d)
		return v, err
	}
	pool, err := d.engine.Open(url, current)
	if err != nil || pool == nil {
		return nil, dbFailure(CodeDatabaseOpen, "DATABASE_OPEN", fmt.Sprintf("database %q (%s at %s) could not be opened by its engine", d.name, engineTitle(d.engineName()), desc.location()), a.dbFields(d, desc)...)
	}
	return pool, nil
}

// tune applies d's tuning to its pool — the SDK refuses a pool policy it
// cannot honour — and returns what its ports are built from.
func (a *App) tune(d *database, pool *stdsql.DB, desc *DatabaseURLValue) (sql.Config, error) {
	cfg := sql.Config{
		DB: pool, Dialect: d.dialect(),
		Pool: sql.PoolConfig{
			MaxOpen: resolvedSetting(a, d.maxOpen), MaxIdle: resolvedSetting(a, d.maxIdle),
			MaxLifetime: resolvedSetting(a, d.maxLifetime), MaxIdleTime: resolvedSetting(a, d.maxIdleTime),
		},
		// A database's budgets are the network's, in wall time: a test's
		// manual clock would hold a check or a migration lock forever.
		Clock:        clock.System,
		CheckTimeout: resolvedSetting(a, d.checkTimeout),
	}
	if _, err := sql.NewChecker(cfg); err != nil {
		return cfg, dbFailure(CodeDatabaseConfig, "DATABASE_POOL", fmt.Sprintf("the pool of database %q cannot be set up: %s", d.name, errs.PublicOf(err)), a.dbFields(d, desc)...)
	}
	return cfg, nil
}

// startDatabase brings a database up: opened, migrated — or its pending
// migrations refusing the start when it migrates manually —, and checked.
// In dev without a URL it opens nothing, and its stores stay in the data
// directory.
func (a *App) startDatabase(ctx context.Context, r *databaseRun) error {
	o, err := a.openDatabase(ctx, r.d)
	if err != nil {
		r.failed(err)
		return err
	}
	if o == nil {
		r.set(model.DatabaseUnset)
		logger.Warn(ctx, a.log, "a database has no URL: its stores stay in the data directory",
			logger.String("database", r.d.name), logger.String("variable", a.urlVariable(r.d)))
		return nil
	}
	r.opened(o)
	if err := a.bringUp(ctx, r, o); err != nil {
		r.failed(err)
		if cerr := r.close(); cerr != nil {
			logger.Warn(ctx, a.log, "a database that did not come up could not be closed", logger.String("database", r.d.name),
				logger.String("error", errs.PublicOf(cerr)))
		}
		return err
	}
	r.set(model.DatabaseOpen)
	logger.Info(ctx, a.log, "database open", logger.String("database", r.d.name), logger.String("engine", r.d.engineName()),
		logger.String("address", o.url.location()), logger.String("tls", tlsWords(&o.url)))
	return nil
}

// bringUp runs an opened database's migrations, then its check.
func (a *App) bringUp(ctx context.Context, r *databaseRun, o *opened) error {
	sets, err := a.migrateAtStart(ctx, r.d, &o.cfg)
	r.mu.Lock()
	r.sets = sets
	r.mu.Unlock()
	if err != nil {
		return err
	}
	return a.checkDatabase(ctx, r)
}

// opened remembers the pool the run opened, and its checker.
func (r *databaseRun) opened(o *opened) {
	// A checker that cannot be built leaves none: every check then reports
	// the database as not open, which is what it is to kit.
	checker, err := sql.NewChecker(o.cfg)
	if err != nil {
		checker = nil
	}
	r.mu.Lock()
	r.url, r.urlFrom, r.pool, r.cfg, r.checker = o.url, o.from, o.pool, o.cfg, checker
	r.mu.Unlock()
}

// set moves the run to state.
func (r *databaseRun) set(state string) {
	r.mu.Lock()
	r.state = state
	r.mu.Unlock()
}

// failed records a failure's public text: never a driver's.
func (r *databaseRun) failed(err error) {
	_, body := describe(err)
	r.mu.Lock()
	r.state, r.ready, r.problem = model.DatabaseFailed, false, errs.PublicOf(err)
	if r.problem == "" {
		r.problem = body.Message
	}
	r.mu.Unlock()
}

// close closes the pool, once.
func (r *databaseRun) close() error {
	r.mu.Lock()
	pool := r.pool
	r.pool, r.checker = nil, nil
	if r.state == model.DatabaseOpen {
		r.state = model.DatabaseClosed
	}
	r.ready = false
	r.mu.Unlock()
	if pool == nil {
		return nil
	}
	return pool.Close()
}

// checkDatabase asks the database whether it answers, within
// <name>-check-timeout, and remembers the answer. A database with no URL in
// dev is not in use: ready. The error is kit's, and says no more than the
// SDK's verdict.
func (a *App) checkDatabase(ctx context.Context, r *databaseRun) error {
	r.mu.Lock()
	state, checker, url := r.state, r.checker, r.url
	r.mu.Unlock()
	if state == model.DatabaseUnset || state == model.DatabaseMemory {
		return nil
	}
	fields := []errs.Field{errs.String("database", r.d.name), errs.String("address", url.location())}
	var err error
	if checker == nil {
		err = dbFailure(CodeDatabaseUnavailable, "DATABASE_CLOSED", fmt.Sprintf("database %q is not open", r.d.name), fields...)
	} else if cerr := checker.Check(ctx); cerr != nil {
		err = dbFailure(CodeDatabaseUnavailable, "DATABASE_UNAVAILABLE",
			fmt.Sprintf("database %q (%s) did not answer: %s", r.d.name, url.location(), errs.PublicOf(cerr)), fields...)
	}
	now := time.Now().UTC()
	r.mu.Lock()
	r.checkedAt, r.ready = now, err == nil
	switch {
	case err == nil && r.state != model.DatabaseFailed:
		r.problem = ""
	case err != nil:
		r.problem = errs.PublicOf(err)
	}
	r.mu.Unlock()
	return err
}

// versionText is a migration's version as a command prints it.
func versionText(v uint64) string { return strconv.FormatUint(v, 10) }
