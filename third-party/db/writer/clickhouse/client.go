// Package clickhouse — the client seam: the ONLY file that imports the database
// driver. newClient validates the config, optionally resolves credentials, and
// builds a lazy database/sql handle via clickhouse-go's OpenDB (no connection
// until the first INSERT). It returns the handle plus the execBatch closure that
// delivers a coalesced batch as one multi-row INSERT, confining the driver here.
package clickhouse

import (
	"context"
	"database/sql"
	"strings"

	chdriver "github.com/ClickHouse/clickhouse-go/v2"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/writer"
)

// colsPerRow is the number of columns each log row writes: (ts, level, message).
const colsPerRow int = 3

// newClient validates the config, resolves optional credentials, and opens a
// lazy database/sql handle over the ClickHouse native protocol. OpenDB does not
// connect — the first INSERT on the drainer goroutine establishes the session.
func newClient(c writer.ClickHouseConfig) (handle *sql.DB, exec func(context.Context, []corelogger.RecordEvent) error, err error) {
	//: the table is interpolated into SQL (it cannot be a placeholder), so
	//: reject anything but a plain identifier to foreclose injection.
	if !validIdent(c.Table) {
		//: surface the client-init sentinel (no value to echo).
		return nil, nil, ClientInitFailed
	}
	//: resolve optional auth once (empty user/pass uses the default user).
	user, pass, cerr := resolveCreds(c.Credentials)
	//: forward a credential-resolution failure under the init sentinel.
	if cerr != nil {
		//: wrapClientInit never echoes the credentials.
		return nil, nil, wrapClientInit(cerr)
	}
	//: build a lazy handle over the native-protocol address.
	db := chdriver.OpenDB(&chdriver.Options{
		Addr: []string{c.Address},
		Auth: chdriver.Auth{Database: c.Database, Username: user, Password: pass},
	})
	table := c.Table
	//: the deliver closure runs on the drainer goroutine; the DB-touching Exec
	//: lives here (anonymous) so the package exposes no untestable named method.
	exec = func(ctx context.Context, batch []corelogger.RecordEvent) error {
		//: an empty batch is a no-op (guard; the batcher never delivers one).
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

// resolveCreds reads the optional credential provider, returning empty
// user/password when none is supplied (ClickHouse then uses the default user).
func resolveCreds(cp writer.CredentialProvider) (user, password string, err error) {
	//: no provider means the driver's default user.
	if cp == nil {
		//: default-user access with no password.
		return "", "", nil
	}
	//: resolve the login once; the handle is static for the pool.
	cred, cerr := cp.Credentials(context.Background())
	//: a provider failure aborts construction.
	if cerr != nil {
		//: surface the cause for newClient to wrap.
		return "", "", cerr
	}
	//: AccessKeyID() is the ClickHouse username, SecretAccessKey() the password.
	return cred.AccessKeyID(), cred.SecretAccessKey(), nil
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
// a digit, or underscore.
func isIdentRune(r rune) bool {
	//: letters, digits, and underscore are the safe identifier alphabet.
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}
