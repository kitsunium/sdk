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

	"github.com/kitsunium/sdk/framework/connectors/mysql"
	"github.com/kitsunium/sdk/framework/kit"
	"github.com/kitsunium/sdk/framework/kit/storetest"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A database on MySQL opens, runs the product's migrations under
// schema_migrations, answers its check and is ready.
func TestMySQLOpensMigratesAndChecks(t *testing.T) {
	t.Setenv("LEDGER_DATABASE_URL", fresh(t))
	var logs syncBuffer
	app := ledger(t, &logs, kit.Migrations(migrations...))
	run(t, app)
	if got := tables(t); !slices.Equal(got, withKitsTables("entries", "schema_migrations", "trail")) {
		t.Errorf("tables %v", got)
	}
	d := databaseOf(t, app.Graph())
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
	if len(d.Migrations) != 2 || d.Migrations[0].Name != "kit" || len(d.Migrations[0].Applied) != 2 ||
		len(d.Migrations[1].Applied) != 2 || len(d.Migrations[1].Pending) != 0 {
		t.Errorf("migrations %+v", d.Migrations)
	}
	if s := status(t, app.URL(), "/_kit/health/ready"); s != http.StatusOK {
		t.Errorf("ready: %d", s)
	}
}

// Two processes starting together apply a set once: the lock of its
// version table serialises them.
func TestTwoProcessesMigrateOnce(t *testing.T) {
	t.Setenv("LEDGER_DATABASE_URL", fresh(t))
	var logs syncBuffer
	apps := []*kit.App{ledger(t, &logs, kit.Migrations(migrations...)), ledger(t, &logs, kit.Migrations(migrations...))}
	var wg sync.WaitGroup
	errsOf := make([]error, len(apps))
	for i, app := range apps {
		wg.Go(func() { errsOf[i] = app.Start(t.Context()) })
	}
	wg.Wait()
	for i, app := range apps {
		if errsOf[i] != nil {
			t.Errorf("process %d: %v", i, errsOf[i])
			continue
		}
		t.Cleanup(func() {
			if err := app.Stop(context.Background()); err != nil {
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
	err := ledger(t, &logs, kit.Migrations(migrations...)).Start(t.Context())
	if !errs.HasCode(err, kit.CodeDatabaseMigrate) || !strings.Contains(err.Error(), "ledger migrate up") {
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
		if code := ledger(t, &logs, kit.Migrations(migrations...)).Main(t.Context(), append([]string{"migrate"}, args...)); code != 0 {
			t.Fatalf("migrate %v: exit %d", args, code)
		}
	}
	migrate("status")
	migrate("up")
	migrate("status")
	if got := tables(t); !slices.Equal(got, withKitsTables("entries", "schema_migrations", "trail")) {
		t.Fatalf("after migrate up: %v", got)
	}
	run(t, ledger(t, &logs, kit.Migrations(migrations...)))
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
	app := ledger(t, &logs)
	run(t, app)
	p.down()
	if s := status(t, app.URL(), "/_kit/health/ready"); s != http.StatusServiceUnavailable {
		t.Errorf("ready while the server is away: %d", s)
	}
	if s := status(t, app.URL(), "/_kit/health/live"); s != http.StatusOK {
		t.Errorf("live while the server is away: %d", s)
	}
	if d := databaseOf(t, app.Graph()); d.Ready || d.Problem == "" {
		t.Errorf("runtime while away %+v", d)
	}
	p.up(t)
	eventually(t, app.URL(), "once the server is back")
}

// A start against a server that does not answer fails, and unwinds.
func TestAnUnreachableMySQLFailsTheStart(t *testing.T) {
	fresh(t)
	p := newProxy(t, server(t).Host)
	p.down()
	t.Setenv("LEDGER_DATABASE_URL", viaHost(t, p.addr))
	var logs syncBuffer
	if err := ledger(t, &logs).Start(t.Context()); !errs.HasCode(err, kit.CodeDatabaseUnavailable) {
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
			app := ledger(t, &logs)
			err := app.Start(t.Context())
			if err == nil {
				if err := app.Stop(context.Background()); err != nil {
					t.Logf("stop: %v", err)
				}
				t.Fatal("started")
			}
			graph, err2 := json.Marshal(app.Graph())
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
	app := ledger(t, &logs)
	run(t, app)
	rotate(t)
	t.Setenv("LEDGER_DATABASE_URL", testURL(t, url.UserPassword(rotationUser, secondWord)))
	time.Sleep(1200 * time.Millisecond)
	eventually(t, app.URL(), "with the rotated password")
	if n := count(t, sessionsOfRotation); n == 0 {
		t.Error("no connection with the rotated password")
	}
	if d := databaseOf(t, app.Graph()); d.Pool == nil || d.Pool.Closed == 0 {
		t.Errorf("no connection retired by its lifetime: %+v", d.Pool)
	}
}

// withKitsTables are the tables of the database the tests' product runs on:
// kit's own — its version table, its registry, the store's table and index
// rows — and the product's.
func withKitsTables(product ...string) []string {
	out := append([]string{"books__entries", "books__entries___ix", "kit_migrations", "kit_tables", "kit_tables___ix"}, product...)
	slices.Sort(out)
	return out
}

// The stores' conformance suite, on MySQL: each case on a fresh database.
func TestStoresConform(t *testing.T) {
	server(t)
	storetest.Run(t, storetest.BackendConfig{Name: "mysql", Options: func(t *testing.T, _ string) []kit.AppOption {
		t.Setenv("STORETEST_DATABASE_URL", fresh(t))
		return []kit.AppOption{kit.DataDir(t.TempDir()), kit.Database("database", mysql.Engine())}
	}})
}
