package kit

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// The floors kit holds a database to at its start (ADR 0178 C2). The SQL
// broker every consumer's queue may live on leases with SKIP LOCKED, which
// MySQL has since 8.0.1 and MariaDB since 10.6 (the SDK's ADR 0151): one
// floor for every MySQL database kit opens, read once with SELECT VERSION(),
// whether or not a queue lives there. A SQLite file has one writer, and a
// connection that finds it taken waits the busy timeout: kit gives it one
// strictly under <name>-timeout, so that a lost race on the lock comes back
// as SQLite's refusal before the call's own deadline, never as a cancelled
// context. PostgreSQL needs no floor beyond the document store's.

const (
	// sqliteTimeoutFloor is the least <name>-timeout a SQLite database
	// takes: under it, the busy timeout would fall under its own margin.
	sqliteTimeoutFloor time.Duration = 500 * time.Millisecond
	// lockMarginFloor is the least of what a call still needs once the lock
	// is granted: a statement on a local file.
	lockMarginFloor time.Duration = 250 * time.Millisecond
)

// serverVersion is a server's version: major, minor, patch.
type serverVersion [3]int

// The MySQL servers kit opens: those that lease with SKIP LOCKED.
var (
	mysqlFloor   = serverVersion{8, 0, 1}
	mariadbFloor = serverVersion{10, 6, 0}
)

// lockWait is the busy timeout kit gives an engine whose connections wait
// for a lock, under t, the database's <name>-timeout: t less a margin of a
// tenth of t, and never less than lockMarginFloor — 4.5s for 5s, 27s for
// 30s, 250ms for 500ms.
func lockWait(t time.Duration) time.Duration {
	return t - max(lockMarginFloor, t/10)
}

// below reports whether v comes before floor.
func (v serverVersion) below(floor serverVersion) bool {
	for i := range v {
		if v[i] != floor[i] {
			return v[i] < floor[i]
		}
	}
	return false
}

// String is the version as a server writes it: "8.0.36".
func (v serverVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}

// mysqlServer reads what SELECT VERSION() answered — "8.0.36",
// "5.7.44-log", "10.6.12-MariaDB-1:10.6.12+maria~ubu2004", MariaDB's
// "5.5.5-" prefix too —: whether the server is MariaDB, and its version.
// ok is false when the answer starts with no version.
func mysqlServer(answer string) (mariadb bool, version serverVersion, ok bool) {
	mariadb = strings.Contains(strings.ToLower(answer), "mariadb")
	answer = strings.TrimPrefix(answer, "5.5.5-")
	number, _, _ := strings.Cut(answer, "-")
	parts := strings.SplitN(number, ".", 3)
	if len(parts) < 2 {
		return mariadb, version, false
	}
	for i, p := range parts {
		digits := p
		if end := strings.IndexFunc(p, func(r rune) bool { return r < '0' || r > '9' }); end >= 0 {
			digits = p[:end]
		}
		n, err := strconv.Atoi(digits)
		if err != nil {
			return mariadb, version, false
		}
		version[i] = n
	}
	return mariadb, version, true
}

// mysqlRefusal is what kit says of a MySQL server whose SELECT VERSION()
// answered answer, below its floor or unreadable; "" when it is at or above
// it. It quotes the server's name and its version, never the rest of the
// answer.
func mysqlRefusal(database, answer string) string {
	mariadb, v, ok := mysqlServer(answer)
	floor, name := mysqlFloor, "MySQL"
	if mariadb {
		floor, name = mariadbFloor, "MariaDB"
	}
	switch {
	case !ok:
		return fmt.Sprintf("database %q does not say a MySQL version kit reads: kit needs MySQL 8.0.1 or MariaDB 10.6", database)
	case v.below(floor):
		return fmt.Sprintf("database %q runs %s %s: kit needs MySQL 8.0.1 or MariaDB 10.6, for SKIP LOCKED", database, name, v)
	}
	return ""
}

// checkServerFloor refuses the start of a MySQL database whose server is
// below kit's floor, read once with SELECT VERSION() within
// <name>-check-timeout. Other engines have none to check.
func (a *App) checkServerFloor(ctx context.Context, r *databaseRun, o *opened) error {
	if r.d.dialect() != sql.DialectMySQL {
		return nil
	}
	fields := a.dbFields(r.d, &o.url)
	ctx, cancel := context.WithTimeout(ctx, resolvedSetting(a, r.d.checkTimeout))
	defer cancel()
	var answer string
	if err := o.pool.QueryRowContext(ctx, "SELECT VERSION()").Scan(&answer); err != nil {
		return dbFailure(CodeDatabaseUnavailable, "DATABASE_VERSION_UNREAD", fmt.Sprintf("database %q did not say its server's version", r.d.name), fields...)
	}
	if refusal := mysqlRefusal(r.d.name, answer); refusal != "" {
		return dbFailure(CodeDatabaseVersion, "DATABASE_VERSION", refusal, fields...)
	}
	return nil
}

// sqliteProblem is what kit refuses of a SQLite database's timing at its
// start, or nil: a <name>-timeout under sqliteTimeoutFloor, or a URL that
// writes a busy timeout — _busy_timeout or _timeout, the two spellings the
// driver reads — of <name>-timeout or more. kit never rewrites the URL's: a
// value the operator wrote and kit replaced would be inert (ADR 0031).
func (a *App) sqliteProblem(d *database, at *model.Source) *model.Diagnostic {
	t := resolvedSetting(a, d.timeout)
	setting := d.timeout.name
	if t < sqliteTimeoutFloor {
		return new(diagnosticOf("error", "", at, say("database.sqlite-timeout", "setting", setting, "timeout", t.String(), "floor", sqliteTimeoutFloor.String())))
	}
	u, _, err := a.databaseURL(context.Background(), d)
	if err != nil {
		return nil
	}
	busy, ok := urlBusyTimeout(u.RevealString())
	if !ok || busy < t {
		return nil
	}
	return new(diagnosticOf("error", "", at, say("database.sqlite-busy-timeout", "database", d.name, "busy", busy.String(), "setting", setting, "timeout", t.String())))
}

// urlBusyTimeout is the busy timeout a SQLite URL writes, in milliseconds
// as the driver reads it — _timeout winning over _busy_timeout, as it does
// there —; ok is false when it writes none, or one the driver refuses.
func urlBusyTimeout(raw string) (time.Duration, bool) {
	_, rawQuery, _ := strings.Cut(strings.TrimPrefix(raw, "file:"), "?")
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return 0, false
	}
	value := ""
	for _, key := range []string{"_busy_timeout", "_timeout"} {
		if query.Has(key) {
			value = query.Get(key)
		}
	}
	ms, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, false
	}
	return time.Duration(ms) * time.Millisecond, true
}
