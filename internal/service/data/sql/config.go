package sql

import (
	stdsql "database/sql"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
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

// resolve validates the Config, applies the pool policy to the caller's DB,
// and returns the clamped form.
func (c Config) resolve() (settings resolved, err error) {
	//: a nil pool cannot be recovered from — there is nothing to talk to.
	if c.DB == nil {
		//: the missing field names which half is absent.
		return settings, kerrs.Wrap(coresql.ConfigInvalid, kerrs.WrapParams{},
			kerrs.String("missing", "db"))
	}
	//: DialectUnknown is what an unset field looks like; reading it as
	//: "probably postgres" is how a MySQL deployment learns the difference.
	if !c.Dialect.Valid() {
		//: name the dialect rather than the DSN.
		return settings, kerrs.Wrap(coresql.ConfigInvalid, kerrs.WrapParams{},
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
		return kerrs.Wrap(coresql.PoolMisconfigured, kerrs.WrapParams{},
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
