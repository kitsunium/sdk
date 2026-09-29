package mysql_test

import (
	"context"
	stdsql "database/sql"
	"net/url"
	"os"
	"testing"

	"github.com/kitsunium/sdk/framework/connectors/mysql"
	"github.com/kitsunium/sdk/pkg/v1/secret"
)

// A database on MySQL, end to end: KIT_TEST_MYSQL_URL names a server kit's
// tests may create and drop a database and a user on — an administrator's
// URL, mysql://root@127.0.0.1:3306/mysql?tls=false. Without it, these tests
// are skipped: `go test ./...` stays green with no database.
//
// Every statement the tests send is a constant, and so is every value they
// bind: SQL built from a variable is how an injection starts, even in a
// test. So the tests' names are fixed — the database kit_test, made afresh
// for each test and dropped after it, and the user of the rotation.

// The statements the tests send. A password is written in none: it is
// bound, and the administrator's connection interpolates it
// (interpolateParams), since MySQL takes no parameter in a CREATE USER.
const (
	dropTestDatabase   = "DROP DATABASE IF EXISTS kit_test"
	createTestDatabase = "CREATE DATABASE kit_test"
	listTables         = "SELECT table_name FROM information_schema.tables WHERE table_schema = 'kit_test' ORDER BY table_name"
	countMigrations    = "SELECT count(*) FROM kit_test.schema_migrations"
	dropRotationUser   = "DROP USER IF EXISTS 'kit_rotation'@'%'"
	createRotationUser = "CREATE USER 'kit_rotation'@'%' IDENTIFIED BY ?"
	grantRotationUser  = "GRANT ALL ON kit_test.* TO 'kit_rotation'@'%'"
	alterRotationUser  = "ALTER USER 'kit_rotation'@'%' IDENTIFIED BY ?"
	sessionsOfRotation = "SELECT count(*) FROM information_schema.processlist WHERE user = 'kit_rotation'"
)

// The rotation's user, and the two words it logs in with.
const (
	rotationUser = "kit_rotation"
	firstWord    = "kit-rotation-5b8e-first"
	secondWord   = "kit-rotation-c3a0-second"
)

// server is the test server's URL, or the test is skipped.
func server(t *testing.T) *url.URL {
	t.Helper()
	raw := os.Getenv("KIT_TEST_MYSQL_URL")
	if raw == "" {
		t.Skip("KIT_TEST_MYSQL_URL is not set: no MySQL to test on")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("KIT_TEST_MYSQL_URL: %v", err)
	}
	return u
}

// admin is a pool on the test server as its administrator, opened by the
// engine itself, which interpolates the values it binds.
func admin(t *testing.T) *stdsql.DB {
	t.Helper()
	s := server(t)
	q := s.Query()
	q.Set("interpolateParams", "true")
	raw := (&url.URL{Scheme: s.Scheme, User: s.User, Host: s.Host, Path: s.Path, RawQuery: q.Encode()}).String()
	v := secret.FromString(raw)
	pool, err := mysql.Engine().Open(v, func(context.Context) (secret.Value, error) { return v, nil })
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
	a := admin(t)
	for _, stmt := range []string{dropTestDatabase, createTestDatabase} {
		if _, err := a.ExecContext(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if _, err := a.ExecContext(context.Background(), dropTestDatabase); err != nil {
			t.Logf("cleaning up: %v", err)
		}
	})
	return testURL(t, nil)
}

// tables are the tables of kit_test.
func tables(t *testing.T) []string {
	t.Helper()
	rows, err := admin(t).QueryContext(t.Context(), listTables)
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

// count is what a counting statement answers, as the administrator.
func count(t *testing.T, stmt string) int {
	t.Helper()
	var n int
	if err := admin(t).QueryRowContext(t.Context(), stmt).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// rotationUserWith makes the rotation's user, with its first word, dropped
// after the test.
func rotationUserWith(t *testing.T) {
	t.Helper()
	a := admin(t)
	steps := []struct {
		stmt string
		args []any
	}{
		{dropRotationUser, nil},
		{createRotationUser, []any{firstWord}},
		{grantRotationUser, nil},
	}
	for _, s := range steps {
		if _, err := a.ExecContext(t.Context(), s.stmt, s.args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if _, err := a.ExecContext(context.Background(), dropRotationUser); err != nil {
			t.Logf("cleaning up: %v", err)
		}
	})
}

// rotate gives the rotation's user its second word: the server refuses the
// first from now on.
func rotate(t *testing.T) {
	t.Helper()
	if _, err := admin(t).ExecContext(t.Context(), alterRotationUser, secondWord); err != nil {
		t.Fatal(err)
	}
}
