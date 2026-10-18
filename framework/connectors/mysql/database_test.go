package mysql_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/framework/app/storetest"
	"github.com/kitsunium/sdk/framework/connectors/mysql"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A database on MySQL opens, runs the product's migrations under
// schema_migrations, answers its check and is ready.
func TestMySQLOpensMigratesAndChecks(t *testing.T) {
	t.Setenv("LEDGER_DATABASE_URL", fresh(t))
	var logs syncBuffer
	a := ledger(t, &logs, app.Migrations(migrations...))
	run(t, a)
	if got := tables(t); !slices.Equal(got, withKitsTables("entries", "schema_migrations", "trail")) {
		t.Errorf("tables %v", got)
	}
	d := databaseOf(t, a.Graph())
	type seen struct {
		state, engine, tls string
		ready              bool
	}
	if got := (seen{d.State, d.Engine, d.TLS, d.Ready}); got != (seen{model.DatabaseOpen, "mysql", "none", true}) {
		t.Errorf("runtime %+v", d)
	}
	if d.Pool == nil || d.Pool.Open == 0 {
		t.Errorf("pool %+v", d.Pool)
	}
	// kit's own set first — its registry, the store's table —, then the
	// product's.
	if len(d.Migrations) != 2 || d.Migrations[0].Name != "app" || len(d.Migrations[0].Applied) != 2 ||
		len(d.Migrations[1].Applied) != 2 || len(d.Migrations[1].Pending) != 0 {
		t.Errorf("migrations %+v", d.Migrations)
	}
	if s := status(t, a.URL(), "/_app/health/ready"); s != http.StatusOK {
		t.Errorf("ready: %d", s)
	}
}

// Two processes starting together apply a set once: the lock of its
// version table serialises them.
func TestTwoProcessesMigrateOnce(t *testing.T) {
	t.Setenv("LEDGER_DATABASE_URL", fresh(t))
	var logs syncBuffer
	apps := []*app.App{ledger(t, &logs, app.Migrations(migrations...)), ledger(t, &logs, app.Migrations(migrations...))}
	var wg sync.WaitGroup
	errsOf := make([]error, len(apps))
	for i, a := range apps {
		wg.Go(func() { errsOf[i] = a.Start(t.Context()) })
	}
	wg.Wait()
	for i, a := range apps {
		if errsOf[i] != nil {
			t.Errorf("process %d: %v", i, errsOf[i])
			continue
		}
		t.Cleanup(func() {
			if err := a.Stop(context.Background()); err != nil {
				t.Logf("stop: %v", err)
			}
		})
	}
	if n := count(t, countMigrations); n != 2 {
		t.Errorf("schema_migrations holds %d rows", n)
	}
}

// With <name>-migrate: manual, pending migrations refuse the start, naming
// the command that runs them.
func TestManualMigrationsWaitForTheCommand(t *testing.T) {
	t.Setenv("LEDGER_DATABASE_URL", fresh(t))
	t.Setenv("LEDGER_DATABASE_MIGRATE", "manual")
	var logs syncBuffer
	err := ledger(t, &logs, app.Migrations(migrations...)).Start(t.Context())
	if !errs.HasCode(err, app.CodeDatabaseMigrate) || !strings.Contains(err.Error(), "ledger migrate up") {
		t.Fatalf("a manual database with pending migrations started: %v", err)
	}
	if got := tables(t); len(got) != 1 {
		t.Errorf("a manual database migrated at start: %v", got)
	}
}

// The product's migrate command says where the sets are, runs them, and
// reverses them.
func TestMigrateOnMySQL(t *testing.T) {
	t.Setenv("LEDGER_DATABASE_URL", fresh(t))
	t.Setenv("LEDGER_DATABASE_MIGRATE", "manual")
	var logs syncBuffer
	migrate := func(args ...string) {
		t.Helper()
		if code := ledger(t, &logs, app.Migrations(migrations...)).Main(t.Context(), append([]string{"migrate"}, args...)); code != 0 {
			t.Fatalf("migrate %v: exit %d", args, code)
		}
	}
	migrate("status")
	migrate("up")
	migrate("status")
	if got := tables(t); !slices.Equal(got, withKitsTables("entries", "schema_migrations", "trail")) {
		t.Fatalf("after migrate up: %v", got)
	}
	run(t, ledger(t, &logs, app.Migrations(migrations...)))
	migrate("down", "product", "0")
	if got := tables(t); !slices.Equal(got, withKitsTables("schema_migrations")) {
		t.Errorf("after migrate down: %v", got)
	}
}

// Readiness follows the database; liveness never depends on it.
func TestReadinessFollowsMySQL(t *testing.T) {
	fresh(t)
	p := newProxy(t, server(t).Host)
	t.Setenv("LEDGER_DATABASE_URL", viaHost(t, p.addr))
	var logs syncBuffer
	a := ledger(t, &logs)
	run(t, a)
	p.down()
	if s := status(t, a.URL(), "/_app/health/ready"); s != http.StatusServiceUnavailable {
		t.Errorf("ready while the server is away: %d", s)
	}
	if s := status(t, a.URL(), "/_app/health/live"); s != http.StatusOK {
		t.Errorf("live while the server is away: %d", s)
	}
	if d := databaseOf(t, a.Graph()); d.Ready || d.Problem == "" {
		t.Errorf("runtime while away %+v", d)
	}
	p.up(t)
	eventually(t, a.URL(), "once the server is back")
}

// A start against a server that does not answer fails, and unwinds.
func TestAnUnreachableMySQLFailsTheStart(t *testing.T) {
	fresh(t)
	p := newProxy(t, server(t).Host)
	p.down()
	t.Setenv("LEDGER_DATABASE_URL", viaHost(t, p.addr))
	var logs syncBuffer
	if err := ledger(t, &logs).Start(t.Context()); !errs.HasCode(err, app.CodeDatabaseUnavailable) {
		t.Fatalf("start: %v", err)
	}
}

// The credentials never leak: a malformed URL, an unreachable host and a
// refused password reach neither the start's error, the graph nor the logs.
func TestMySQLCredentialsNeverLeak(t *testing.T) {
	fresh(t)
	const sentinel, user = "s3ntinel-my-51c0", "kit_nobody"
	s := server(t)
	p := newProxy(t, s.Host)
	p.down()
	for name, raw := range map[string]string{
		"malformed":   "mysql://" + user + ":" + sentinel + "@" + s.Hostname() + ":notaport/kit_test?tls=false",
		"unreachable": "mysql://" + user + ":" + sentinel + "@" + p.addr + "/kit_test?tls=false",
		"refused":     "mysql://" + user + ":" + sentinel + "@" + s.Host + "/kit_test?tls=false",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("LEDGER_DATABASE_URL", raw)
			var logs syncBuffer
			a := ledger(t, &logs)
			err := a.Start(t.Context())
			if err == nil {
				if err := a.Stop(context.Background()); err != nil {
					t.Logf("stop: %v", err)
				}
				t.Fatal("started")
			}
			graph, err2 := json.Marshal(a.Graph())
			if err2 != nil {
				t.Fatal(err2)
			}
			for where, text := range map[string]string{"the start error": err.Error(), "the graph": string(graph), "the logs": logs.String()} {
				if strings.Contains(text, sentinel) || strings.Contains(text, user) {
					t.Errorf("%s discloses the credentials: %s", where, text)
				}
			}
		})
	}
}

// A password rotated where it lives is used by the next connection: the
// engine takes the credentials of the URL as it is now, and
// <name>-max-lifetime retires the connection opened with the previous one.
// The server refuses the first password once it changed, so a connection
// it lets in was made with the second.
func TestARotatedPasswordReachesTheNextConnection(t *testing.T) {
	fresh(t)
	rotationUserWith(t)
	t.Setenv("LEDGER_DATABASE_URL", testURL(t, url.UserPassword(rotationUser, firstWord)))
	t.Setenv("LEDGER_DATABASE_MAX_LIFETIME", "1s")
	var logs syncBuffer
	a := ledger(t, &logs)
	run(t, a)
	rotate(t)
	t.Setenv("LEDGER_DATABASE_URL", testURL(t, url.UserPassword(rotationUser, secondWord)))
	// The lifetime is the behaviour: the connection made with the first
	// password must have outlived it before anything is asked.
	time.Sleep(1200 * time.Millisecond)
	eventually(t, a.URL(), "with the rotated password")
	// The pool retires and redials on its own goroutines: wait for both
	// facts to be observed, within a bound, rather than reading each once.
	deadline := time.Now().Add(10 * time.Second)
	for {
		n := count(t, sessionsOfRotation)
		d := databaseOf(t, a.Graph())
		if n > 0 && d.Pool != nil && d.Pool.Closed > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the rotation: %d connection(s) with the rotated password, pool %+v; want one, and one retired by its lifetime", n, d.Pool)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// withKitsTables are the tables of the database the tests' product runs on:
// kit's own — its version table, its registry, the store's table and index
// rows — and the product's.
func withKitsTables(product ...string) []string {
	out := append([]string{"app_migrations", "app_tables", "app_tables___ix", "books__entries", "books__entries___ix"}, product...)
	slices.Sort(out)
	return out
}

// The stores' conformance suite, on MySQL: each case on a fresh database.
func TestStoresConform(t *testing.T) {
	server(t)
	storetest.Run(t, storetest.BackendConfig{Name: "mysql", Options: func(t *testing.T, _ string) []app.AppOption {
		t.Setenv("STORETEST_DATABASE_URL", fresh(t))
		return []app.AppOption{app.DataDir(t.TempDir()), app.Database("database", mysql.Engine())}
	}})
}
