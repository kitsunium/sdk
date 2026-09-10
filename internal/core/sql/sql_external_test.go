// Package sql_test verifies the port contract itself: the method set that
// makes a commit unwritable, the closed dialect set, and the migration value's
// refusals.
package sql_test

import (
	"context"
	"math"
	"reflect"
	"slices"
	"testing"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestExecutorHasNoWayToEndATransaction is the executable form of ADR 0055
// §D3. Transaction ownership is claimed to be STRUCTURAL — a callee cannot
// commit because the type it receives has no such method — and a claim like
// that is worth exactly as much as the test that fails when someone widens
// the interface "just for one case".
func TestExecutorHasNoWayToEndATransaction(t *testing.T) {
	t.Parallel()
	executor := reflect.TypeFor[coresql.Executor]()
	got := make([]string, 0, executor.NumMethod())
	for method := range executor.Methods() {
		got = append(got, method.Name)
	}
	slices.Sort(got)
	want := []string{"ExecContext", "QueryContext", "QueryRowContext"}
	if !slices.Equal(got, want) {
		t.Fatalf("Executor method set = %v, want exactly %v", got, want)
	}
	for _, forbidden := range []string{"Commit", "Rollback", "Begin", "BeginTx"} {
		if _, found := executor.MethodByName(forbidden); found {
			t.Fatalf("Executor exposes %s: a callee could end its caller's transaction", forbidden)
		}
	}
}

// TestTransactorIsFrozenAtOneMethod pins the ADR 0039 promise: pkg/v1 aliases
// this interface, so a second method would break every downstream double at
// compile time with no deprecation window.
func TestTransactorIsFrozenAtOneMethod(t *testing.T) {
	t.Parallel()
	if got := reflect.TypeFor[coresql.Transactor]().NumMethod(); got != 1 {
		t.Fatalf("Transactor has %d methods, want 1 — options travel in TxOptionsValue, not in a sibling method", got)
	}
}

// TestParseDialectRefusesRecognisedEnginesByName covers the distinction that
// makes a refusal actionable: an engine we KNOW and decline reports why, and
// is a different error from one we have never heard of.
func TestParseDialectRefusesRecognisedEnginesByName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"sqlserver", "mssql", "oracle", "godror", "db2"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dialect, err := coresql.ParseDialect(name)
			if !errs.HasCode(err, coresql.CodeDialectRefused) {
				t.Fatalf("ParseDialect(%q) = %v, want DIALECT_REFUSED", name, err)
			}
			if dialect.Valid() {
				t.Fatalf("ParseDialect(%q) returned a usable dialect alongside its refusal", name)
			}
			if !hasField(errs.FieldsOf(err), "reason") {
				t.Fatalf("ParseDialect(%q) refused without saying why", name)
			}
		})
	}
}

// TestParseDialectRefusesUnknownNames proves the domain never guesses: an
// unrecognised name is refused rather than defaulted to the most popular
// engine.
func TestParseDialectRefusesUnknownNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "postgress", "cassandra", "POSTGRES"} {
		dialect, err := coresql.ParseDialect(name)
		if !errs.HasCode(err, coresql.CodeUnknownDialect) {
			t.Fatalf("ParseDialect(%q) = (%v, %v), want UNKNOWN_DIALECT", name, dialect, err)
		}
	}
}

// TestParseDialectAcceptsTheThreeSupportedEngines pins the accepted set and
// its aliases.
func TestParseDialectAcceptsTheThreeSupportedEngines(t *testing.T) {
	t.Parallel()
	//: a slice rather than a map: the accepted set is ORDERED prose (the
	//: canonical name first, then its aliases) and a map would shuffle it.
	cases := []struct {
		name string
		want coresql.Dialect
	}{
		{"postgres", coresql.DialectPostgres},
		{"postgresql", coresql.DialectPostgres},
		{"pgx", coresql.DialectPostgres},
		{"mysql", coresql.DialectMySQL},
		{"mariadb", coresql.DialectMySQL},
		{"sqlite", coresql.DialectSQLite},
		{"sqlite3", coresql.DialectSQLite},
	}
	for _, tc := range cases {
		name, want := tc.name, tc.want
		got, err := coresql.ParseDialect(name)
		if err != nil || got != want {
			t.Fatalf("ParseDialect(%q) = (%v, %v), want (%v, nil)", name, got, err, want)
		}
	}
}

// TestOnlyPostgresAndMySQLClaimAnAdvisoryLock pins the capability the
// migration runner refuses on. SQLite has no advisory-lock function, and
// claiming otherwise would make the runner's mutual-exclusion promise false
// exactly where it matters.
func TestOnlyPostgresAndMySQLClaimAnAdvisoryLock(t *testing.T) {
	t.Parallel()
	cases := map[coresql.Dialect]bool{
		coresql.DialectPostgres: true, coresql.DialectMySQL: true,
		coresql.DialectSQLite: false, coresql.DialectUnknown: false,
	}
	for dialect, want := range cases {
		if got := dialect.SupportsAdvisoryLock(); got != want {
			t.Fatalf("%v.SupportsAdvisoryLock() = %v, want %v", dialect, got, want)
		}
	}
}

// TestZeroDialectIsUnusable pins ADR 0031's zero-value rule on the one field
// where guessing would be silent: an unset dialect must not read as "probably
// postgres".
func TestZeroDialectIsUnusable(t *testing.T) {
	t.Parallel()
	var zero coresql.Dialect
	if zero.Valid() {
		t.Fatal("the zero Dialect reports itself usable")
	}
	if zero.String() != "unknown" {
		t.Fatalf("zero Dialect renders as %q, want \"unknown\"", zero.String())
	}
}

// TestMigrationRefusesEveryHalfItCannotRunWithout covers rule-by-rule what
// Validate rejects, including the nil Down that Irreversible exists to make
// explicit.
func TestMigrationRefusesEveryHalfItCannotRunWithout(t *testing.T) {
	t.Parallel()
	step := func(context.Context, coresql.Executor) error { return nil }
	//: a slice, so the "missing" half each case exercises is listed in the
	//: order Validate checks them and a failure names the first rule to fire.
	cases := []struct {
		missing   string
		migration coresql.MigrationValue
	}{
		{"version", coresql.MigrationValue{Version: 0, Name: "n", Up: step, Down: step}},
		{"name", coresql.MigrationValue{Version: 1, Up: step, Down: step}},
		{"up", coresql.MigrationValue{Version: 1, Name: "n", Down: step}},
		{"down", coresql.MigrationValue{Version: 1, Name: "n", Up: step}},
		{"version-fits-int64", coresql.MigrationValue{
			Version: math.MaxUint64, Name: "n", Up: step, Down: step,
		}},
	}
	for _, tc := range cases {
		missing, migration := tc.missing, tc.migration
		err := migration.Validate()
		if !errs.HasCode(err, coresql.CodeInvalidMigration) {
			t.Fatalf("missing %s: Validate() = %v, want INVALID_MIGRATION", missing, err)
		}
		if got := fieldValue(errs.FieldsOf(err), "missing"); got != missing {
			t.Fatalf("missing %s: field said %q", missing, got)
		}
	}
}

// TestAValidMigrationValidates is the positive half — a rule that rejects
// everything is not a rule.
func TestAValidMigrationValidates(t *testing.T) {
	t.Parallel()
	step := func(context.Context, coresql.Executor) error { return nil }
	migration := coresql.MigrationValue{Version: 20260910143000, Name: "create accounts", Up: step, Down: step}
	if err := migration.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// TestIrreversibleRefusesWithItsOwnCode pins that a caller's explicit
// "cannot be reversed" comes back as a typed, matchable answer rather than as
// a nil dereference.
func TestIrreversibleRefusesWithItsOwnCode(t *testing.T) {
	t.Parallel()
	err := coresql.Irreversible(t.Context(), nil)
	if !errs.HasCode(err, coresql.CodeMigrationIrreversible) {
		t.Fatalf("Irreversible() = %v, want MIGRATION_IRREVERSIBLE", err)
	}
}

// TestZeroTxOptionsMeanTheDriverDefault pins the value IsZero decides on,
// because that is what makes a nested call's refusal correct.
func TestZeroTxOptionsMeanTheDriverDefault(t *testing.T) {
	t.Parallel()
	if !(coresql.TxOptionsValue{}).IsZero() {
		t.Fatal("the zero TxOptionsValue does not report itself zero")
	}
	if (coresql.TxOptionsValue{ReadOnly: true}).IsZero() {
		t.Fatal("a read-only request reports itself zero, so nesting would silently accept it")
	}
}

// TestNoPublicMessageDescribesInfrastructure is the executable half of the
// rule that a driver's world never reaches a wire-safe message. It scans this
// package's sentinels for the vocabulary a leak would use.
func TestNoPublicMessageDescribesInfrastructure(t *testing.T) {
	t.Parallel()
	sentinels := []error{
		coresql.UnknownDialect, coresql.DialectRefused, coresql.NestedIsolation,
		coresql.InvalidMigration, coresql.MigrationIrreversible,
	}
	banned := []string{"password", "host=", "://", "SELECT", "INSERT", "dsn"}
	for _, sentinel := range sentinels {
		public := errs.PublicOf(sentinel)
		for _, word := range banned {
			if containsFold(public, word) {
				t.Fatalf("Public %q contains %q — a wire-safe message must not describe infrastructure", public, word)
			}
		}
	}
}

// hasField reports whether a key is present in an error's fields.
func hasField(fields []errs.FieldValue, key string) bool {
	for _, field := range fields {
		if field.Key() == key {
			return true
		}
	}
	return false
}

// fieldValue returns the string value of a key, or "".
func fieldValue(fields []errs.FieldValue, key string) string {
	for _, field := range fields {
		if field.Key() == key {
			return field.StringValue()
		}
	}
	return ""
}

// containsFold reports a case-insensitive substring match without pulling in
// a dependency for one call.
func containsFold(haystack, needle string) bool {
	return len(needle) <= len(haystack) && indexFold(haystack, needle) >= 0
}

// indexFold is the case-insensitive strings.Index this file needs.
func indexFold(haystack, needle string) int {
	for start := 0; start+len(needle) <= len(haystack); start++ {
		if equalFold(haystack[start:start+len(needle)], needle) {
			return start
		}
	}
	return -1
}

// equalFold compares two equal-length ASCII strings case-insensitively.
func equalFold(a, b string) bool {
	for index := range len(a) {
		if lower(a[index]) != lower(b[index]) {
			return false
		}
	}
	return true
}

// lower folds one ASCII byte.
func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}
