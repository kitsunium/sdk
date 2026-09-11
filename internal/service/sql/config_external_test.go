// Package sql_test — the pool-policy suite: what is refused, what is clamped,
// and the observable proof that both actually reached the pool.
package sql_test

import (
	"testing"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsql "github.com/kitsunium/sdk/internal/service/sql"
)

// TestANonPositiveMaxOpenIsRefused pins ADR 0031's refusal side. database/sql
// reads 0 as UNLIMITED, and an unset field must not mean "open as many
// connections to the database as this process feels like".
func TestANonPositiveMaxOpenIsRefused(t *testing.T) {
	t.Parallel()
	for _, value := range []int{0, -1} {
		db := closeOnCleanup(t, newFakeDB().open())
		_, err := svcsql.NewTransactor(svcsql.Config{
			DB: db, Dialect: coresql.DialectPostgres,
			Pool: svcsql.PoolConfig{MaxOpen: value},
		})
		if !errs.HasCode(err, svcsql.CodePoolMisconfigured) {
			t.Fatalf("MaxOpen=%d: NewTransactor = %v, want POOL_MISCONFIGURED", value, err)
		}
	}
}

// TestARefusedPoolPolicyLeavesTheCallersDBUntouched pins that a rejected
// Config is not half-applied — the caller's pool is exactly as they handed it
// over.
func TestARefusedPoolPolicyLeavesTheCallersDBUntouched(t *testing.T) {
	t.Parallel()
	db := closeOnCleanup(t, newFakeDB().open())
	db.SetMaxOpenConns(7)
	//: the refusal is the precondition of this test, so it is asserted rather
	//: than assumed: if the constructor ever started accepting MaxOpen: 0,
	//: the assertion below would pass for the wrong reason.
	if _, err := svcsql.NewTransactor(svcsql.Config{
		DB: db, Dialect: coresql.DialectPostgres, Pool: svcsql.PoolConfig{MaxOpen: 0},
	}); !errs.HasCode(err, svcsql.CodePoolMisconfigured) {
		t.Fatalf("NewTransactor(MaxOpen: 0) = %v, want POOL_MISCONFIGURED", err)
	}
	if got := db.Stats().MaxOpenConnections; got != 7 {
		t.Fatalf("MaxOpenConnections = %d after a refused Config, want the caller's 7", got)
	}
}

// TestTheClampsReachThePool is the observable half. A clamp that is only
// documented is a comment; this asserts the value database/sql is actually
// holding.
func TestTheClampsReachThePool(t *testing.T) {
	t.Parallel()
	db := closeOnCleanup(t, newFakeDB().open())
	//: MaxIdle above MaxOpen is reduced by database/sql SILENTLY, which is
	//: why the SDK does it first — a caller reading their config back must
	//: see the value that is in force.
	if _, err := svcsql.NewTransactor(svcsql.Config{
		DB: db, Dialect: coresql.DialectPostgres,
		Pool: svcsql.PoolConfig{MaxOpen: 3, MaxIdle: 99},
	}); err != nil {
		t.Fatalf("NewTransactor: %v", err)
	}
	stats := db.Stats()
	if stats.MaxOpenConnections != 3 {
		t.Fatalf("MaxOpenConnections = %d, want 3", stats.MaxOpenConnections)
	}
	if stats.MaxIdleClosed != 0 {
		t.Fatalf("MaxIdleClosed = %d before any use", stats.MaxIdleClosed)
	}
}

// TestConfigRefusesAnUnsetDialect pins that DialectUnknown never reads as
// "probably postgres".
func TestConfigRefusesAnUnsetDialect(t *testing.T) {
	t.Parallel()
	db := closeOnCleanup(t, newFakeDB().open())
	_, err := svcsql.NewTransactor(svcsql.Config{DB: db, Pool: svcsql.PoolConfig{MaxOpen: 2}})
	if !errs.HasCode(err, svcsql.CodeConfigInvalid) {
		t.Fatalf("NewTransactor = %v, want CONFIG_INVALID", err)
	}
}

// TestConfigRefusesANilPool pins the other required field. This package never
// opens a pool, because opening one means importing a driver.
func TestConfigRefusesANilPool(t *testing.T) {
	t.Parallel()
	_, err := svcsql.NewTransactor(svcsql.Config{Dialect: coresql.DialectPostgres})
	if !errs.HasCode(err, svcsql.CodeConfigInvalid) {
		t.Fatalf("NewTransactor = %v, want CONFIG_INVALID", err)
	}
}

// TestTheDocumentedDefaultsAreNamedValues pins that every clamp target is an
// exported constant. A default nobody can name is a default nobody can reason
// about — a caller sizing a container's grace period needs the number, and a
// test asserting the clamp must not copy a literal.
func TestTheDocumentedDefaultsAreNamedValues(t *testing.T) {
	t.Parallel()
	if svcsql.DefaultMaxLifetime <= 0 {
		t.Fatal("DefaultMaxLifetime is not finite — an immortal connection survives a failover")
	}
	if svcsql.DefaultCheckTimeout <= 0 {
		t.Fatal("DefaultCheckTimeout is not finite — a health check that can hang is not one")
	}
	if svcsql.DefaultMaxIdle <= 0 {
		t.Fatal("DefaultMaxIdle is not positive")
	}
	if svcsql.DefaultLockTimeout <= 0 || svcsql.DefaultLockRetryInterval <= 0 {
		t.Fatal("the migration-lock budgets are not finite")
	}
	if svcsql.DefaultLockRetryInterval >= svcsql.DefaultLockTimeout {
		t.Fatal("the retry interval is not shorter than the budget, so only one attempt would ever be made")
	}
	if svcsql.DefaultMaxLifetime < time.Minute {
		t.Fatal("DefaultMaxLifetime is short enough to churn the pool")
	}
}
