// Package sql — hosts Config, the parameters every port in this package is
// built from, and the pool policy it applies.
package sql

import (
	stdsql "database/sql"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// DefaultMaxLifetime is the connection lifetime a non-positive
// [PoolConfig.MaxLifetime] clamps to.
//
// database/sql reads 0 as "reuse a connection forever", and a connection that
// lives forever survives a failover, a DNS change and a credential rotation —
// it keeps talking to a replica that has been demoted, and nothing in the
// application ever notices. The exact number matters far less than its being
// FINITE, so this is a clamp rather than a refusal (ADR 0031): thirty minutes
// is long enough to be invisible on the hot path and short enough that a
// topology change is picked up without a restart.
const DefaultMaxLifetime time.Duration = 30 * time.Minute

// DefaultMaxIdle is the idle-connection ceiling a non-positive
// [PoolConfig.MaxIdle] clamps to — the same value database/sql itself uses, so
// the clamp changes nothing a caller could have observed.
const DefaultMaxIdle int = 2

// DefaultCheckTimeout is the liveness-probe budget a non-positive
// [Config.CheckTimeout] clamps to. A health check that can hang forever is
// not a health check.
const DefaultCheckTimeout time.Duration = 5 * time.Second

// PoolConfig is the connection-pool policy. It is applied to the caller's
// *sql.DB at construction, and it is a POLICY rather than a set of hints: a
// field whose zero value database/sql would read as a decision nobody made is
// either clamped or refused, never passed through (ADR 0031).
type PoolConfig struct {
	// MaxOpen is the hard ceiling on open connections. It is REQUIRED and
	// must be positive.
	//
	// database/sql reads 0 as UNLIMITED. "As many connections as the
	// application happens to want" is a decision made by forgetting a line,
	// and the consequence lands on the database rather than on the process
	// that forgot — every replica racing to exhaust one server's
	// max_connections. No value the SDK could invent is defensible either,
	// because the right one depends on the server's ceiling divided by the
	// number of replicas, which the SDK cannot see. So this is the REFUSAL
	// side of ADR 0031.
	MaxOpen int
	// MaxIdle is the number of connections kept warm. A non-positive value
	// clamps to [DefaultMaxIdle]; a value above MaxOpen clamps DOWN to
	// MaxOpen, because database/sql does that reduction silently and a caller
	// reading their own configuration back would otherwise be misled.
	MaxIdle int
	// MaxLifetime is how long a connection may be reused. A non-positive
	// value clamps to [DefaultMaxLifetime]; see that constant for why an
	// unbounded lifetime is not an option.
	MaxLifetime time.Duration
	// MaxIdleTime is how long an unused connection is kept. A non-positive
	// value is passed through as "no idle deadline", and that is deliberate
	// rather than an oversight: MaxLifetime is now always finite, so an idle
	// connection is already retired on a bounded schedule, and a second
	// timer would only decide how aggressively to shrink a warm pool — a
	// tuning choice with no dangerous zero.
	MaxIdleTime time.Duration
}

// Config parameterises every constructor in this package: the caller's pool,
// the dialect whose grammar the SDK will speak to it, the pool policy applied
// at construction, the time source every budget is read from, and the health
// probe's budget.
//
// It is passed BY VALUE on purpose. A constructor must not be able to observe
// a later mutation of the caller's struct, and the copy happens once per port
// rather than once per statement.
type Config struct {
	// DB is the caller's pool. REQUIRED — this package never opens one,
	// because opening one means importing a driver (ADR 0055 §D2).
	DB *stdsql.DB
	// Dialect selects the SQL grammar. REQUIRED and must be a dialect this
	// SDK can spell; resolve a configuration string through
	// core/sql.ParseDialect, which refuses the engines it cannot support BY
	// NAME instead of guessing.
	Dialect coresql.Dialect
	// Pool is the connection-pool policy, applied to DB at construction.
	Pool PoolConfig
	// Clock is the time source every budget in this package is read from. A
	// nil Clock falls back to clock.System.
	//
	// clock.Timed rather than clock.Clock because this package both stamps
	// (the version table's applied_at) and WAITS (the probe budget, the lock
	// retry). Reading package time instead would make every budget assertion
	// a sleep, and a sleep a tolerance, and a tolerance a flake.
	Clock clock.Timed
	// CheckTimeout is the budget one [core/sql.Checker.Check] gets. A
	// non-positive value clamps to [DefaultCheckTimeout].
	CheckTimeout time.Duration
}

// resolve validates the Config, applies the pool policy to the caller's DB,
// and returns the clamped form.
func (c Config) resolve() (settings resolved, err error) {
	//: a nil pool cannot be recovered from — there is nothing to talk to.
	if c.DB == nil {
		//: the missing field names which half is absent.
		return settings, kerrs.Wrap(ConfigInvalid, kerrs.WrapParams{},
			kerrs.String("missing", "db"))
	}
	//: DialectUnknown is what an unset field looks like; reading it as
	//: "probably postgres" is how a MySQL deployment learns the difference.
	if !c.Dialect.Valid() {
		//: name the dialect rather than the DSN.
		return settings, kerrs.Wrap(ConfigInvalid, kerrs.WrapParams{},
			kerrs.String("missing", "dialect"), kerrs.String("dialect", c.Dialect.String()))
	}
	//: the pool policy is refused before anything is applied, so a rejected
	//: Config leaves the caller's DB exactly as they handed it over.
	if err := c.Pool.apply(c.DB); err != nil {
		//: propagate POOL_MISCONFIGURED unchanged.
		return settings, err
	}
	//: every remaining field has a documented fallback.
	settings = resolved{db: c.DB, dialect: c.Dialect, clk: c.resolvedClock(), probe: c.probeBudget()}
	//: validated and clamped.
	return settings, nil
}

// resolvedClock applies the documented time-source fallback.
func (c Config) resolvedClock() clock.Timed {
	//: the wall clock is the only non-arbitrary default.
	if c.Clock == nil {
		//: never mutate clock.System at package scope.
		return clock.System
	}
	//: the caller's injected clock.
	return c.Clock
}

// probeBudget applies the documented health-check clamp.
func (c Config) probeBudget() time.Duration {
	//: a non-positive budget is an unset field, not a request for zero time.
	if c.CheckTimeout <= 0 {
		//: the documented floor.
		return DefaultCheckTimeout
	}
	//: the caller's own budget.
	return c.CheckTimeout
}

// apply validates the pool policy and installs it on db.
func (p PoolConfig) apply(db *stdsql.DB) error {
	//: the one field with no defensible default — refuse rather than invent.
	if p.MaxOpen <= 0 {
		//: the value is the caller's own, so naming it leaks nothing.
		return kerrs.Wrap(PoolMisconfigured, kerrs.WrapParams{},
			kerrs.String("field", "MaxOpen"), kerrs.Int("value", p.MaxOpen))
	}
	db.SetMaxOpenConns(p.MaxOpen)
	db.SetMaxIdleConns(p.idleCeiling())
	db.SetConnMaxLifetime(p.lifetime())
	//: a non-positive idle deadline is database/sql's own "no deadline", and
	//: it is safe here because the lifetime above is always finite.
	db.SetConnMaxIdleTime(p.MaxIdleTime)
	//: the policy is installed.
	return nil
}

// idleCeiling applies both documented idle clamps.
func (p PoolConfig) idleCeiling() int {
	//: an unset field takes database/sql's own default, so the clamp changes
	//: nothing a caller could have observed.
	if p.MaxIdle <= 0 {
		//: never keep more warm than the ceiling allows.
		return min(DefaultMaxIdle, p.MaxOpen)
	}
	//: database/sql reduces this silently; doing it here makes the value a
	//: caller reads back the value that is in force.
	return min(p.MaxIdle, p.MaxOpen)
}

// lifetime applies the documented lifetime clamp.
func (p PoolConfig) lifetime() time.Duration {
	//: forever is not a lifetime — see DefaultMaxLifetime.
	if p.MaxLifetime <= 0 {
		//: the documented floor.
		return DefaultMaxLifetime
	}
	//: the caller's own bound.
	return p.MaxLifetime
}
