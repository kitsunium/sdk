package postgres_test

import (
	"context"
	stdsql "database/sql"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// A database on PostgreSQL, end to end: KIT_TEST_POSTGRES_URL names a server
// kit's tests may create and drop a database and roles on — a superuser's
// URL, postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable. Without
// it, these tests are skipped: `go test ./...` stays green with no database.
//
// Every statement the tests send is a constant, and so is every value they
// bind: SQL built from a variable is how an injection starts, even in a
// test. So the tests' names are fixed — the database kit_test, made afresh
// for each test and dropped after it, and the roles of the rotation.

// The statements the tests send.
const (
	dropTestDatabase   = "DROP DATABASE IF EXISTS kit_test WITH (FORCE)"
	createTestDatabase = "CREATE DATABASE kit_test"
	listTables         = "SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' ORDER BY table_name"
	countMigrations    = "SELECT count(*) FROM schema_migrations"
	dropRotationRoles  = "DROP ROLE IF EXISTS kit_rotation_first, kit_rotation_second"
	createFirstRole    = "CREATE ROLE kit_rotation_first LOGIN PASSWORD $1"
	createSecondRole   = "CREATE ROLE kit_rotation_second LOGIN PASSWORD $1"
	grantRotationRoles = "GRANT ALL ON SCHEMA public TO kit_rotation_first, kit_rotation_second"
	sessionsOfSecond   = "SELECT count(*) FROM pg_stat_activity WHERE datname = 'kit_test' AND usename = 'kit_rotation_second'"
)

// The rotation's two roles, and the words they log in with: a password is
// written in no statement, it is bound — through pgx's simple protocol,
// since PostgreSQL takes no parameter in a CREATE ROLE.
const (
	firstRole  = "kit_rotation_first"
	firstWord  = "kit-rotation-7f1c-first"
	secondRole = "kit_rotation_second"
	secondWord = "kit-rotation-02ad-second"
)

// server is the test server's URL, or the test is skipped.
func server(t *testing.T) *url.URL {
	t.Helper()
	raw := os.Getenv("KIT_TEST_POSTGRES_URL")
	if raw == "" {
		t.Skip("KIT_TEST_POSTGRES_URL is not set: no PostgreSQL to test on")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("KIT_TEST_POSTGRES_URL: %v", err)
	}
	return u
}

// open is a pool on raw, closed with the test: pgx's database/sql driver,
// which the engine registers.
func open(t *testing.T, raw string) *stdsql.DB {
	t.Helper()
	pool, err := stdsql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Logf("closing the pool: %v", err)
		}
	})
	return pool
}

// testURL is the URL of kit_test on the test server, as user when one is
// given.
func testURL(t *testing.T, user *url.Userinfo) string {
	t.Helper()
	s := server(t)
	if user == nil {
		user = s.User
	}
	return (&url.URL{Scheme: s.Scheme, User: user, Host: s.Host, Path: "/kit_test", RawQuery: s.RawQuery}).String()
}

// viaHost is the URL of kit_test through another address: a proxy's.
func viaHost(t *testing.T, host string) string {
	t.Helper()
	s := server(t)
	return (&url.URL{Scheme: s.Scheme, User: s.User, Host: host, Path: "/kit_test", RawQuery: s.RawQuery}).String()
}

// fresh makes kit_test afresh for one test, dropped after it, and returns
// its URL.
func fresh(t *testing.T) string {
	t.Helper()
	admin := open(t, server(t).String())
	for _, stmt := range []string{dropTestDatabase, createTestDatabase} {
		if _, err := admin.ExecContext(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), dropTestDatabase); err != nil {
			t.Logf("cleaning up: %v", err)
		}
	})
	return testURL(t, nil)
}

// tables are the tables of kit_test.
func tables(t *testing.T) []string {
	t.Helper()
	rows, err := open(t, testURL(t, nil)).QueryContext(t.Context(), listTables)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	return out
}

// countOn is what a counting statement answers on the pool raw names.
func countOn(t *testing.T, raw, stmt string) int {
	t.Helper()
	var n int
	if err := open(t, raw).QueryRowContext(t.Context(), stmt).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// rotationRoles makes the rotation's two roles, dropped after the test.
func rotationRoles(t *testing.T) {
	t.Helper()
	admin := open(t, server(t).String())
	steps := []struct {
		stmt string
		args []any
	}{
		{dropRotationRoles, nil},
		{createFirstRole, []any{pgx.QueryExecModeSimpleProtocol, firstWord}},
		{createSecondRole, []any{pgx.QueryExecModeSimpleProtocol, secondWord}},
	}
	for _, s := range steps {
		if _, err := admin.ExecContext(t.Context(), s.stmt, s.args...); err != nil {
			t.Fatal(err)
		}
	}
	// kit's own migrations make the store's tables as the first role: the
	// schema of kit_test is the roles' to create in.
	if _, err := open(t, testURL(t, nil)).ExecContext(t.Context(), grantRotationRoles); err != nil {
		t.Fatal(err)
	}
	// The database first: a role that holds a grant in it cannot be dropped.
	t.Cleanup(func() {
		for _, stmt := range []string{dropTestDatabase, dropRotationRoles} {
			if _, err := admin.ExecContext(context.Background(), stmt); err != nil {
				t.Logf("cleaning up: %v", err)
			}
		}
	})
}
