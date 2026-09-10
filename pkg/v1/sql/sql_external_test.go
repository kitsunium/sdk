// Package sql_test verifies the public facade: that the aliases really are
// aliases, that the ergonomic helper passes the options it claims to, and
// that the domain's refusals survive the trip to pkg/v1.
package sql_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	pkgerrs "github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// TestParseDialectRefusalsSurviveTheFacade pins that a consumer gets the same
// two distinct answers the domain makes: an engine we decline, and a name we
// do not know.
func TestParseDialectRefusalsSurviveTheFacade(t *testing.T) {
	t.Parallel()
	if _, err := sql.ParseDialect("sqlserver"); !errors.Is(err, sql.DialectRefused) {
		t.Fatalf("ParseDialect(sqlserver) = %v, want DialectRefused", err)
	}
	if _, err := sql.ParseDialect("nosuchengine"); !errors.Is(err, sql.UnknownDialect) {
		t.Fatalf("ParseDialect(nosuchengine) = %v, want UnknownDialect", err)
	}
	dialect, err := sql.ParseDialect("postgres")
	if err != nil || dialect != sql.DialectPostgres {
		t.Fatalf("ParseDialect(postgres) = (%v, %v)", dialect, err)
	}
}

// TestNewTransactorRefusesAnIncompleteConfig pins that the pool policy's
// refusal reaches a consumer rather than being swallowed by the facade.
func TestNewTransactorRefusesAnIncompleteConfig(t *testing.T) {
	t.Parallel()
	if _, err := sql.NewTransactor(sql.Config{}); !errors.Is(err, sql.ConfigInvalid) {
		t.Fatalf("NewTransactor(zero) = %v, want ConfigInvalid", err)
	}
}

// TestTransactPassesTheZeroOptions pins what the ergonomic helper actually
// does. It is the only behaviour the helper has, so it is the only thing
// worth asserting — and asserting it here is what stops someone "improving"
// it into a read-only default.
func TestTransactPassesTheZeroOptions(t *testing.T) {
	t.Parallel()
	spy := &recordingTransactor{}
	if err := sql.Transact(t.Context(), spy, nil); err != nil {
		t.Fatalf("Transact: %v", err)
	}
	if !spy.opts.IsZero() {
		t.Fatalf("Transact passed %+v, want the zero options", spy.opts)
	}
}

// TestMigrationValidatesThroughTheAlias pins that Migration really is the
// core value type — a copy would drift.
func TestMigrationValidatesThroughTheAlias(t *testing.T) {
	t.Parallel()
	migration := sql.Migration{Version: 1, Name: "one", Up: sql.Statements("SELECT 1")}
	if err := migration.Validate(); !errors.Is(err, sql.InvalidMigration) {
		t.Fatalf("Validate() with a nil Down = %v, want InvalidMigration", err)
	}
	migration.Down = sql.Irreversible
	if err := migration.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil once Down is stated out loud", err)
	}
}

// TestIrreversibleIsMatchableFromTheFacade pins that a caller can route on the
// declaration without importing anything internal.
func TestIrreversibleIsMatchableFromTheFacade(t *testing.T) {
	t.Parallel()
	if err := sql.Irreversible(t.Context(), nil); !errors.Is(err, sql.MigrationIrreversible) {
		t.Fatalf("Irreversible() = %v, want MigrationIrreversible", err)
	}
}

// TestTheDocumentedCodeMatchingActuallyCompiles pins the two spellings the
// package documentation offers a consumer. A doc comment naming a symbol that
// does not exist is the divergence CLAUDE.md rule 11 is about, and the only
// way to keep it honest is to run it.
func TestTheDocumentedCodeMatchingActuallyCompiles(t *testing.T) {
	t.Parallel()
	//: the refusal a consumer can actually provoke without a database.
	_, err := sql.NewTransactor(sql.Config{})
	if !errors.Is(err, sql.ConfigInvalid) {
		t.Fatalf("NewTransactor(zero) = %v, want ConfigInvalid by errors.Is", err)
	}
	//: the same verdict, reached by its ADR 0005 dotted quad.
	if !pkgerrs.HasCode(err, sql.ConfigInvalid.Code()) {
		t.Fatalf("HasCode(%v, ConfigInvalid.Code()) = false, want true", err)
	}
}

// TestNoPublicMessageReachesTheConsumerWithInfrastructure pins the security
// property at the layer that matters — the one a consumer renders to a user.
// The Public half is wire-safe by contract, so it must never spell a host, a
// DSN, a table or a SQL keyword.
func TestNoPublicMessageReachesTheConsumerWithInfrastructure(t *testing.T) {
	t.Parallel()
	//: a slice, so the report walks the facade's sentinels in DECLARATION
	//: order — core's five first, then service's seventeen — and a failure
	//: points at the block the offending Public was written in.
	sentinels := []struct {
		name     string
		sentinel error
	}{
		{"UnknownDialect", sql.UnknownDialect},
		{"DialectRefused", sql.DialectRefused},
		{"NestedIsolation", sql.NestedIsolation},
		{"InvalidMigration", sql.InvalidMigration},
		{"MigrationIrreversible", sql.MigrationIrreversible},
		{"ConfigInvalid", sql.ConfigInvalid},
		{"PoolMisconfigured", sql.PoolMisconfigured},
		{"BeginFailed", sql.BeginFailed},
		{"CommitFailed", sql.CommitFailed},
		{"RollbackFailed", sql.RollbackFailed},
		{"SavepointFailed", sql.SavepointFailed},
		{"TxPoisoned", sql.TxPoisoned},
		{"TxClosed", sql.TxClosed},
		{"HealthCheckFailed", sql.HealthCheckFailed},
		{"HealthCheckTimeout", sql.HealthCheckTimeout},
		{"MigrationFailed", sql.MigrationFailed},
		{"MigrationOutOfOrder", sql.MigrationOutOfOrder},
		{"MigrationLockUnsupported", sql.MigrationLockUnsupported},
		{"MigrationLockTimeout", sql.MigrationLockTimeout},
		{"MigrationUnknownVersion", sql.MigrationUnknownVersion},
		{"VersionTableInvalid", sql.VersionTableInvalid},
		{"DuplicateMigration", sql.DuplicateMigration},
	}
	//: the maximum a Public may be, per CLAUDE.md rule 4.
	const maxPublic int = 120
	for _, tc := range sentinels {
		name, public := tc.name, pkgerrs.PublicOf(tc.sentinel)
		if public == "" {
			t.Errorf("%s: Public is empty", name)
		}
		if runes := len([]rune(public)); runes > maxPublic {
			t.Errorf("%s: Public is %d runes, want <= %d", name, runes, maxPublic)
		}
		if strings.ContainsAny(public, "\n\r") {
			t.Errorf("%s: Public carries a newline", name)
		}
		//: the words a leak would spell. A Public naming any of them is
		//: describing infrastructure rather than a class of failure.
		for _, banned := range []string{
			"postgres://", "mysql://", "@", "SELECT", "INSERT", "DELETE", "password", "dsn",
		} {
			if strings.Contains(strings.ToLower(public), strings.ToLower(banned)) {
				t.Errorf("%s: Public %q contains %q", name, public, banned)
			}
		}
	}
}

// recordingTransactor captures the options the facade passed through.
type recordingTransactor struct {
	// opts is the options value Transact was called with.
	opts sql.TxOptions
}

// Transact records the options and runs nothing.
func (r *recordingTransactor) Transact(_ context.Context, opts sql.TxOptions, _ sql.TxFunc) error {
	r.opts = opts
	return nil
}
