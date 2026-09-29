//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /framework/connectors/sqlite .

// Package sqlite is kit's SQLite engine (ADR 0004): the one module a kit
// product imports to keep a database in a SQLite file, and the only code of
// the product that imports a SQLite driver — modernc.org/sqlite, "a CGo-free
// port of the C SQLite3 library", so a kit product stays one static binary
// (CGO_ENABLED=0). The product's main declares the database with it:
//
//	import "github.com/kitsunium/sdk/framework/connectors/sqlite"
//
//	var App = kit.NewApp("vigie", intake.Service, audit.Service).With(
//		kit.Database("archive", sqlite.Engine(), kit.Keeps(audit.Service)),
//	)
//
// Its URL, in the variable <APP>_<NAME>_URL, is the file's path — or a
// file: URI, with the driver's parameters —; without one, kit keeps the
// database in <data>/<name>.sqlite. The engine opens the file in WAL mode
// with a busy timeout, and its transactions take the write lock when they
// begin (_txlock=immediate), so two writers queue rather than fail; a
// parameter the URL writes itself wins.
//
// On dragonfly, illumos and solaris, which modernc.org/sqlite does not port
// to, the module builds and Open refuses with proc.UnsupportedPlatform.
//
// kit cannot run migrations on SQLite yet: the SDK's migrator refuses a
// dialect with no advisory lock, and its SQLite migrator — the file's own
// lock — is an SDK change to come.
package sqlite

import (
	"context"
	stdsql "database/sql"
	"net/url"
	"strings"

	"github.com/kitsunium/sdk/framework/kit"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/proc"
	"github.com/kitsunium/sdk/pkg/v1/secret"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

const (
	// CodeURLMalformed identifies a URL the driver cannot read (ADR 0147: layer 4,
	// one PP per connector).
	CodeURLMalformed errs.Code = 0x00_04_12_01 // 0.4.18.1

	// driver is how the graph names what reads the file.
	driver string = "modernc.org/sqlite"
)

var (
	// errMalformed is a URL the engine cannot read. It never quotes the URL.
	errMalformed = errs.New(CodeURLMalformed, "URL_MALFORMED", "sqlite: the URL is not a file's path",
		"sqlite connector: the driver refused the URL; the URL is never quoted, the driver's text would name its user and host")

	// defaults are the parameters the engine opens every file with, unless the
	// URL writes its own: WAL, so readers do not wait for a writer; a busy
	// timeout, so a writer waits for another; and the write lock taken when a
	// transaction begins, so two writers queue instead of failing on upgrade.
	defaults = []struct{ key, value string }{
		{"_journal_mode", "WAL"},
		{"_busy_timeout", "5000"},
		{"_txlock", "immediate"},
	}
)

// engine is the connector kit.Database drives; it holds nothing.
type engine struct{}

// Engine is the SQLite engine, for kit.Database.
func Engine() kit.Engine { return engine{} }

// Dialect is the SQL the database speaks.
func (engine) Dialect() sql.Dialect { return sql.DialectSQLite }

// Describe says which file the URL names. There is no network to cross, no
// user and no password.
func (engine) Describe(u secret.Value) (kit.DatabaseURL, error) {
	path, _, err := split(u.RevealString())
	if err != nil {
		return kit.DatabaseURL{}, err
	}
	return kit.DatabaseURL{Driver: driver, Database: path}, nil
}

// Open opens the file, creating it if need be, with the engine's defaults.
// A file has no credentials to rotate: current is not asked.
func (engine) Open(u secret.Value, _ func(context.Context) (secret.Value, error)) (*stdsql.DB, error) {
	if !driverLinked {
		// modernc.org/sqlite has no port to this GOOS (driver_none.go).
		return nil, proc.UnsupportedPlatform
	}
	name, err := dsn(u.RevealString())
	if err != nil {
		return nil, err
	}
	return stdsql.Open("sqlite", name)
}

// dsn is what the driver opens: the file, the URL's parameters, and the
// engine's defaults the URL does not write.
func dsn(raw string) (string, error) {
	path, query, err := split(raw)
	if err != nil {
		return "", err
	}
	for _, d := range defaults {
		if !query.Has(d.key) {
			query.Set(d.key, d.value)
		}
	}
	return path + "?" + query.Encode(), nil
}

// split reads the file's path and the driver's parameters from a path, or
// from a file: URI.
func split(raw string) (string, url.Values, error) {
	raw = strings.TrimPrefix(raw, "file:")
	path, rawQuery, _ := strings.Cut(raw, "?")
	if path == "" {
		return "", nil, errMalformed
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", nil, errMalformed
	}
	return path, query, nil
}
