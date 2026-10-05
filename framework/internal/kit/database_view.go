package kit

import (
	"net/http"
	"slices"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// What the graph and the Studio see of a database (ADR 0004): its state,
// its readiness, its pool as database/sql counts it, its migrations, and its
// TLS mode — never its URL.

// describeDatabases is runtime.databases: each database as this run found
// it, its pool sampled now.
func (a *App) describeDatabases() []model.Database {
	runs := a.databaseRuns()
	if len(runs) == 0 {
		return nil
	}
	out := make([]model.Database, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.describe())
	}
	return out
}

// describe is the database as runtime.databases says it.
func (r *databaseRun) describe() model.Database {
	r.mu.Lock()
	defer r.mu.Unlock()
	db := model.Database{
		Name: r.d.name, Engine: r.d.engineName(), State: r.state, Ready: r.ready,
		Problem: r.problem, URLFrom: r.urlFrom, TLS: r.url.tlsMode(), Migrations: slices.Clone(r.sets),
	}
	if !r.checkedAt.IsZero() {
		at := r.checkedAt
		db.CheckedAt = &at
	}
	if r.pool != nil {
		s := r.pool.Stats()
		db.Pool = &model.Pool{
			MaxOpen: s.MaxOpenConnections, Open: s.OpenConnections, InUse: s.InUse, Idle: s.Idle,
			Waits: s.WaitCount, WaitMs: round2(float64(s.WaitDuration) / float64(time.Millisecond)),
			Closed: s.MaxIdleClosed + s.MaxIdleTimeClosed + s.MaxLifetimeClosed,
		}
	}
	return db
}

// serveDatabases answers GET /_kit/api/databases, in dev: each database
// sampled now — a new check, its migrations read again, its pool.
func (a *App) serveDatabases(w http.ResponseWriter, r *http.Request) {
	for _, run := range a.databaseRuns() {
		run.mu.Lock()
		state, cfg := run.state, run.cfg
		run.mu.Unlock()
		if state != model.DatabaseOpen {
			continue
		}
		// The check records its verdict on the run, which the answer shows.
		if err := a.checkDatabase(r.Context(), run); err != nil {
			logger.Debug(r.Context(), a.log, "a database did not answer the Studio's check", logger.String("database", run.d.name),
				logger.String("error", errs.PublicOf(err)))
		}
		var sets []model.MigrationSet
		for _, set := range a.migrationSets(run.d) {
			m, err := sql.NewMigrator(cfg, sql.MigrateConfig{Migrations: set.migrations, VersionTable: set.table})
			if err != nil {
				sets = append(sets, model.MigrationSet{Name: set.name, Table: set.table, Problem: errs.PublicOf(err)})
				continue
			}
			sets = append(sets, readSet(r.Context(), set, m))
		}
		run.mu.Lock()
		run.sets = sets
		run.mu.Unlock()
	}
	writeJSON(w, http.StatusOK, a.describeDatabases())
}

// tlsWords says a database's TLS mode as the diagram and the logs do.
func tlsWords(u *DatabaseURLValue) string {
	switch {
	case u.Plaintext:
		return "no TLS"
	case u.TLS != "":
		return "TLS " + u.TLS
	case u.Networked:
		return "TLS left to the driver"
	}
	return ""
}
