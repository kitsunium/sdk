//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /framework/connectors/postgres .

// Package postgres is kit's PostgreSQL engine (ADR 0004): the one module a
// kit product imports to keep a database on PostgreSQL, and the only code of
// the product that imports a PostgreSQL driver — pgx, through database/sql.
// The product's main declares the database with it:
//
//	import "github.com/kitsunium/sdk/framework/connectors/postgres"
//
//	var App = kit.NewApp("vigie", intake.Service, desk.Service).With(
//		kit.Database("database", postgres.Engine()),
//	)
//
// The database's URL is libpq's, as pgx reads it —
// postgres://user:password@host:5432/database?sslmode=verify-full, or
// host=… user=… key=value pairs — in the variable <APP>_<NAME>_URL. Outside
// dev kit refuses one that does not write its sslmode: pgx's default,
// prefer, falls back to plaintext, and the weak modes let a network attacker
// impersonate the server. sslmode=disable is accepted when written, for a
// private network. The engine reads the URL again before each new
// connection, so a password rotated where it lives is used by the next one.
package postgres

import (
	"context"
	stdsql "database/sql"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/kitsunium/sdk/framework/kit"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/secret"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

const (
	// CodeURLMalformed identifies a URL the driver cannot read (ADR 0143: layer 4,
	// one PP per connector).
	CodeURLMalformed errs.Code = 0x00_04_10_01 // 0.4.16.1

	// driver is how the graph names what speaks to the server.
	driver string = "pgx"
)

var (
	// errMalformed is a URL pgx cannot read. It never quotes the URL: pgx's own
	// error would name the user and the host.
	errMalformed = errs.New(CodeURLMalformed, "URL_MALFORMED", "postgres: the URL is not one pgx reads",
		"postgres connector: the driver refused the URL; the URL is never quoted, the driver's text would name its user and host")

	// keyValueSSLMode finds sslmode in libpq's key=value form: a bare value, or
	// one in single quotes.
	keyValueSSLMode = regexp.MustCompile(`(?:^|\s)sslmode\s*=\s*('(?:[^'\\]|\\.)*'|\S+)`)
)

// engine is the connector kit.Database drives; it holds nothing.
type engine struct{}

// Engine is the PostgreSQL engine, for kit.Database.
func Engine() kit.Engine { return engine{} }

// Dialect is the SQL the database speaks.
func (engine) Dialect() sql.Dialect { return sql.DialectPostgres }

// Describe says where the URL points — host:port, or the socket's
// directory; the database; the sslmode as the URL writes it — and never who
// connects or with what password.
func (engine) Describe(u secret.Value) (kit.DatabaseURL, error) {
	raw := u.RevealString()
	cfg, err := pgx.ParseConfig(raw)
	if err != nil {
		return kit.DatabaseURL{}, errMalformed
	}
	mode, err := sslmode(raw)
	if err != nil {
		return kit.DatabaseURL{}, errMalformed
	}
	d := kit.DatabaseURL{Driver: driver, Database: cfg.Database, TLS: mode, Plaintext: mode == "disable"}
	if strings.HasPrefix(cfg.Host, "/") {
		// A Unix socket: no network to cross, no TLS to write.
		d.Address = cfg.Host
		return d, nil
	}
	d.Address, d.Networked = net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))), true
	return d, nil
}

// Open opens the pool the URL names, through pgx's database/sql driver.
// Before each new connection it asks current for the URL as it is now and
// takes its credentials: a password rotated where it lives is used by the
// next connection, and <name>-max-lifetime bounds how long one opened with
// the previous password lives.
func (engine) Open(u secret.Value, current func(context.Context) (secret.Value, error)) (*stdsql.DB, error) {
	cfg, err := pgx.ParseConfig(u.RevealString())
	if err != nil {
		return nil, errMalformed
	}
	return stdlib.OpenDB(*cfg, stdlib.OptionBeforeConnect(func(ctx context.Context, c *pgx.ConnConfig) error {
		now, err := current(ctx)
		if err != nil {
			return err
		}
		fresh, err := pgx.ParseConfig(now.RevealString())
		if err != nil {
			return errMalformed
		}
		withCredentials(c, fresh)
		return nil
	})), nil
}

// sslmode is the TLS mode the URL writes, "" when it writes none: pgx then
// uses its default, or PGSSLMODE — neither of which the URL states.
func sslmode(raw string) (string, error) {
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", err
		}
		return u.Query().Get("sslmode"), nil
	}
	m := keyValueSSLMode.FindStringSubmatch(raw)
	if m == nil {
		return "", nil
	}
	return strings.Trim(m[1], "'"), nil
}
