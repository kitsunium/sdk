// Package mysql — the client seam: the ONLY file that imports the database
// driver. newClient resolves credentials once, opens a lazy database/sql handle
// over a Unix-socket DSN, and returns it plus the execBatch closure that
// delivers a coalesced batch as one multi-row INSERT. Keeping the driver import
// here confines go-sql-driver/mysql to a single file.
package mysql

import (
	"context"
	"database/sql"
	"strings"

	driver "github.com/go-sql-driver/mysql"
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/writer"
)

// colsPerRow is the number of columns each log row writes: (ts, level, message).
const colsPerRow int = 3

// newClient validates the config, resolves credentials once (the DSN is static
// for the connection pool), and opens a lazy database/sql handle. It returns the
// handle (so the sink can Close it) plus the deliver closure. sql.Open does not
// connect — the first INSERT on the drainer goroutine establishes the connection.
func newClient(c writer.MySQLConfig) (handle *sql.DB, exec func(context.Context, []corelogger.RecordEvent) error, err error) {
	//: credentials are mandatory (a nil provider cannot authenticate) and the
	//: table must be a plain identifier (it is interpolated into SQL, not bound,
	//: so a non-identifier could inject) — both are misconfigurations.
	if c.Credentials == nil || !validIdent(c.Table) {
		//: surface the client-init sentinel (no value to echo).
		return nil, nil, ClientInitFailed
	}
	//: resolve the login once; a provider failure aborts construction.
	cred, cerr := c.Credentials.Credentials(context.Background())
	//: forward the credential-resolution failure under the init sentinel.
	if cerr != nil {
		//: wrapClientInit never echoes the DSN/credentials.
		return nil, nil, wrapClientInit(cerr)
	}
	//: open a lazy handle over the net=unix DSN the driver formats + escapes.
	db, oerr := sql.Open("mysql", buildDSN(c, cred.AccessKeyID(), cred.SecretAccessKey()))
	//: a malformed DSN surfaces here (still no connection attempted).
	if oerr != nil {
		//: forward under the init sentinel.
		return nil, nil, wrapClientInit(oerr)
	}
	table := c.Table
	//: the deliver closure runs on the drainer goroutine; the DB-touching Exec
	//: lives here (anonymous) so the package exposes no untestable named method.
	exec = func(ctx context.Context, batch []corelogger.RecordEvent) error {
		//: an empty batch is a no-op (the batcher never delivers one, but guard).
		if len(batch) == 0 {
			//: nothing to insert.
			return nil
		}
		query, args := buildInsert(table, batch)
		//: a failed INSERT surfaces under the insert sentinel with the row count.
		if _, eerr := db.ExecContext(ctx, query, args...); eerr != nil {
			//: wrapInsert never echoes the payload.
			return wrapInsert(eerr, len(batch))
		}
		//: the batch landed.
		return nil
	}
	//: hand back the handle (for Close) + the deliver seam.
	return db, exec, nil
}

// buildDSN formats a net=unix MySQL DSN from the config + resolved credentials.
// The driver's Config.FormatDSN escapes every field, so no manual quoting is
// needed; ParseTime makes DATETIME columns scan into time.Time.
func buildDSN(c writer.MySQLConfig, user, password string) string {
	cfg := driver.NewConfig()
	//: local-protocol: connect over the Unix domain socket, never TCP.
	cfg.Net = "unix"
	cfg.Addr = c.SocketPath
	cfg.DBName = c.Database
	//: the caller maps AccessKeyID()->user and SecretAccessKey()->password.
	cfg.User = user
	cfg.Passwd = password
	//: scan DATETIME into time.Time on the read path.
	cfg.ParseTime = true
	//: the driver formats + escapes the full DSN.
	return cfg.FormatDSN()
}

// buildInsert renders the multi-row INSERT for a batch: one placeholder triple
// per record, with the flattened arg list. Pure (no DB) so it is unit-tested
// directly; the table name is pre-validated by newClient.
func buildInsert(table string, batch []corelogger.RecordEvent) (query string, args []any) {
	var b strings.Builder
	//: fixed three-column schema: ts, level, message (documented in CLAUDE.md).
	b.WriteString("INSERT INTO ")
	b.WriteString(table)
	b.WriteString(" (ts, level, message) VALUES ")
	args = make([]any, 0, len(batch)*colsPerRow)
	//: one (?,?,?) tuple per record, comma-separated.
	for i, r := range batch {
		//: separate tuples after the first.
		if i > 0 {
			//: comma between value tuples.
			b.WriteByte(',')
		}
		b.WriteString("(?,?,?)")
		//: flatten the row's bound values in column order.
		args = append(args, r.Time, r.Level.String(), r.Message)
	}
	//: hand back the rendered statement + its bound args.
	return b.String(), args
}

// validIdent reports whether name is a plain SQL identifier ([A-Za-z0-9_]+), the
// only shape safe to interpolate into the INSERT (table names cannot be bound
// parameters). An empty name is rejected.
func validIdent(name string) bool {
	//: an empty identifier is never valid.
	if name == "" {
		//: reject the empty table name.
		return false
	}
	//: every rune must be an identifier character.
	for _, r := range name {
		//: a non-identifier rune fails the whole name.
		if !isIdentRune(r) {
			//: reject the name on the first invalid rune.
			return false
		}
	}
	//: every rune passed — a safe identifier.
	return true
}

// isIdentRune reports whether r is a plain SQL-identifier character: a letter,
// a digit, or underscore. Split out of validIdent so the loop stays simple and
// the positive form avoids a De Morgan rewrite.
func isIdentRune(r rune) bool {
	//: letters, digits, and underscore are the safe identifier alphabet.
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}
