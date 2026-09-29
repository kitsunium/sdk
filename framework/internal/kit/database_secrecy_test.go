package kit_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// A database's URL is a secret: it never leaks, a rotated one reaches the
// next connection, and the Studio sees where it points, never what it is.

// The credentials never leak: a malformed URL, an unreachable host and a
// refused password reach neither the start's error, the graph, the logs nor
// the Studio.
func TestDatabaseCredentialsNeverLeak(t *testing.T) {
	const sentinel = "s3ntinel-db-9f2e"
	const user = "ledgerowner"
	for name, c := range map[string]struct {
		url      string
		down     bool
		password string
	}{
		"malformed":   {url: "fake://" + user + ":" + sentinel + "@db.internal:notaport/ledger?tls=verify-full"},
		"unreachable": {url: fakeURL(user, sentinel, "tls=verify-full"), down: true},
		"refused":     {url: fakeURL(user, sentinel, "tls=verify-full"), password: "the-right-one"},
	} {
		t.Run(name, func(t *testing.T) {
			db := kit.NewFakeDB(sql.DialectPostgres)
			db.SetDown(c.down)
			db.SetPassword(c.password)
			t.Setenv("LEDGER_DATABASE_URL", c.url)
			var logs syncBuffer
			app := ledgerApp(t, newLedger(), &logs, kit.Database("database", db.Engine()), kit.Env(kit.EnvDev), kit.Analyze(false))
			err := app.Start(t.Context())
			if err == nil {
				if err := app.Stop(context.Background()); err != nil {
					t.Fatal(err)
				}
				t.Fatal("started")
			}
			raw, rawErr := json.Marshal(app.Graph())
			if rawErr != nil {
				t.Fatal(rawErr)
			}
			for where, text := range map[string]string{"the start error": err.Error(), "the graph": string(raw), "the logs": logs.String()} {
				if strings.Contains(text, sentinel) || strings.Contains(text, user) {
					t.Errorf("%s discloses the credentials: %s", where, text)
				}
			}
		})
	}
}

// A password rotated where it lives is used by the next connection: the
// engine asks for the URL as it is now.
func TestARotatedPasswordReachesTheNextConnection(t *testing.T) {
	needsFileStore(t)
	db := kit.NewFakeDB(sql.DialectPostgres)
	t.Setenv("LEDGER_DATABASE_URL", fakeURL("ledger", "first", "tls=verify-full"))
	var logs syncBuffer
	app := ledgerApp(t, newLedger(), &logs, kit.Database("database", db.Engine()))
	run(t, app)
	t.Setenv("LEDGER_DATABASE_URL", fakeURL("ledger", "second", "tls=verify-full"))
	db.Retire()
	if r := call(t, app, "GET /_kit/health/ready", noBody); r.status != http.StatusOK {
		t.Fatalf("ready: %d %s", r.status, r.body)
	}
	if logins := db.Logins(); len(logins) < 2 || logins[0] != "first" || logins[len(logins)-1] != "second" {
		t.Errorf("logins %v", logins)
	}
}

// In dev the Studio samples the databases: a fresh check, the pool, the
// migrations; a location, in dev only — never the URL.
func TestTheStudioSamplesTheDatabases(t *testing.T) {
	needsFileStore(t)
	db := kit.NewFakeDB(sql.DialectPostgres)
	t.Setenv("LEDGER_DATABASE_URL", fakeURL("ledger", "pw", ""))
	var logs syncBuffer
	app := ledgerApp(t, newLedger(), &logs, kit.Database("database", db.Engine(), kit.Migrations(migrations...)), kit.Env(kit.EnvDev), kit.Analyze(false))
	run(t, app)
	r := call(t, app, "GET /_kit/api/databases", noBody)
	var got []model.Database
	r.json(t, &got)
	if r.status != http.StatusOK || len(got) != 1 || !got[0].Ready || got[0].Pool == nil || len(got[0].Migrations) != 1 || len(got[0].Migrations[0].Applied) != 2 {
		t.Fatalf("databases: %d %s", r.status, r.body)
	}
	if strings.Contains(string(r.body), "pw@") {
		t.Error("the Studio sees the URL")
	}
	c := containerByID(app.Graph(), "container:database:database")
	if c.Location != "db.internal:5432/ledger" || c.Technology != "PostgreSQL · fake · TLS left to the driver" {
		t.Errorf("container %+v", c)
	}
}
