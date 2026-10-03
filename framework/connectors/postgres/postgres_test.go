package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/connectors/postgres"
	"github.com/kitsunium/sdk/framework/kit"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// Describe says where a URL points and in which TLS mode, never who
// connects: the graph, the Studio and the logs read it.
func TestDescribeSaysWhereNeverWho(t *testing.T) {
	const sentinel = "s3ntinel-pg-4c1b"
	for name, c := range map[string]struct {
		url  string
		want kit.DatabaseURL
	}{
		"url": {
			"postgres://owner:" + sentinel + "@db.internal:6543/vigie?sslmode=verify-full",
			kit.DatabaseURL{Driver: "pgx", Address: "db.internal:6543", Database: "vigie", TLS: "verify-full", Networked: true},
		},
		"default port": {
			"postgresql://owner:" + sentinel + "@db.internal/vigie?sslmode=require",
			kit.DatabaseURL{Driver: "pgx", Address: "db.internal:5432", Database: "vigie", TLS: "require", Networked: true},
		},
		"no tls written": {
			"postgres://owner:" + sentinel + "@db.internal/vigie",
			kit.DatabaseURL{Driver: "pgx", Address: "db.internal:5432", Database: "vigie", Networked: true},
		},
		"tls off": {
			"postgres://owner:" + sentinel + "@10.0.0.7:5432/vigie?sslmode=disable",
			kit.DatabaseURL{Driver: "pgx", Address: "10.0.0.7:5432", Database: "vigie", TLS: "disable", Plaintext: true, Networked: true},
		},
		"key=value": {
			"host=db.internal port=5433 dbname=vigie user=owner password=" + sentinel + " sslmode='verify-ca'",
			kit.DatabaseURL{Driver: "pgx", Address: "db.internal:5433", Database: "vigie", TLS: "verify-ca", Networked: true},
		},
		"socket": {
			"host=/var/run/postgresql dbname=vigie user=owner",
			kit.DatabaseURL{Driver: "pgx", Address: "/var/run/postgresql", Database: "vigie"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := postgres.Engine().Describe(secret.FromString(c.url))
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("described as %+v, want %+v", got, c.want)
			}
		})
	}
	_, err := postgres.Engine().Describe(secret.FromString("postgres://owner:" + sentinel + "@db.internal:notaport/vigie"))
	if err == nil || strings.Contains(err.Error(), sentinel) || strings.Contains(err.Error(), "owner") {
		t.Errorf("a malformed URL: %v", err)
	}
	if _, err := postgres.Engine().Open(secret.FromString("postgres://owner:"+sentinel+"@db.internal:notaport/vigie"), nil); err == nil || strings.Contains(err.Error(), sentinel) {
		t.Errorf("opening a malformed URL: %v", err)
	}
}

func TestTheEngineSpeaksPostgres(t *testing.T) {
	if postgres.Engine().Dialect() != sql.DialectPostgres {
		t.Fatal(postgres.Engine().Dialect())
	}
	// A pool opens without a connection: the first one is made on use.
	db, err := postgres.Engine().Open(secret.FromString("postgres://owner@127.0.0.1:1/vigie?sslmode=disable"),
		func(context.Context) (secret.Value, error) {
			return secret.FromString("postgres://owner@127.0.0.1:1/vigie?sslmode=disable"), nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Error(err)
	}
}
