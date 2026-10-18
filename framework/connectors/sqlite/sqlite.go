package sqlite

import (
	"context"
	stdsql "database/sql"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kitsunium/sdk/framework/app"
	_ "github.com/kitsunium/sdk/framework/app/store" // the stores a database keeps (ADR 0179 §4)
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/proc"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

const (
	// driver is how the graph names what reads the file.
	driver string = "modernc.org/sqlite"
	// busyKey is the busy timeout's parameter, in milliseconds.
	busyKey string = "_busy_timeout"
)

var (
	// errMalformed is a URL the engine cannot read. It never quotes the URL.
	errMalformed = errs.New(CodeURLMalformed, "URL_MALFORMED", "sqlite: the URL is not a file's path",
		"sqlite connector: the driver refused the URL; the URL is never quoted, the driver's text would name its user and host")

	// defaults are the parameters the engine opens every file with, unless the
	// URL writes its own under either spelling the driver reads: WAL, so
	// readers do not wait for a writer; a busy timeout, so a writer waits for
	// another; and the write lock taken when a transaction begins, so two
	// writers queue instead of failing on upgrade.
	defaults = []struct{ key, alias, value string }{
		{"_journal_mode", "_journal", "WAL"},
		{busyKey, "_timeout", "5000"},
		{"_txlock", "", "immediate"},
	}
)

// engine is the connector app.Database drives; it holds nothing.
type engine struct{}

// Engine is the SQLite engine, for app.Database.
func Engine() app.Engine { return engine{} }

// Dialect is the SQL the database speaks.
func (engine) Dialect() app.Dialect { return app.DialectSQLite }

// Describe says which file the URL names. There is no network to cross, no
// user and no password.
func (engine) Describe(u secret.Value) (app.DatabaseURL, error) {
	path, _, err := split(u.RevealString())
	if err != nil {
		return app.DatabaseURL{}, err
	}
	return app.DatabaseURL{Driver: driver, Database: path}, nil
}

// Open opens the file, creating it if need be, with the engine's defaults —
// a busy timeout of five seconds. A file has no credentials to rotate:
// current is not asked.
func (e engine) Open(u secret.Value, current func(context.Context) (secret.Value, error)) (*stdsql.DB, error) {
	return e.OpenWaiting(u, current, 0)
}

// OpenWaiting opens the file as Open does, its connections waiting wait for
// the file's write lock — kit's busy timeout, under <name>-timeout (ADR
// 0178 C2) — when the URL writes none; zero keeps the engine's default.
func (engine) OpenWaiting(u secret.Value, _ func(context.Context) (secret.Value, error), wait time.Duration) (*stdsql.DB, error) {
	if !driverLinked {
		// modernc.org/sqlite has no port to this GOOS (driver_none.go).
		return nil, proc.UnsupportedPlatform
	}
	name, err := dsn(u.RevealString(), wait)
	if err != nil {
		return nil, err
	}
	return stdsql.Open("sqlite", name)
}

// dsn is what the driver opens: the file, the URL's parameters, and the
// engine's defaults the URL does not write — the busy timeout wait, in
// milliseconds, when it is not zero.
func dsn(raw string, wait time.Duration) (string, error) {
	path, query, err := split(raw)
	if err != nil {
		return "", err
	}
	for _, d := range defaults {
		if query.Has(d.key) || (d.alias != "" && query.Has(d.alias)) {
			continue
		}
		value := d.value
		if d.key == busyKey && wait > 0 {
			value = strconv.FormatInt(wait.Milliseconds(), 10)
		}
		query.Set(d.key, value)
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
