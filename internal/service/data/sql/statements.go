package sql

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
)

// Statements returns a [coresql.Step] that runs stmts in order on the
// transaction's executor, stopping at the first failure.
//
// This is the smallest helper that is genuinely useful and deliberately not a
// file loader. Reading a directory would mean inventing a filename grammar
// (`0001_name.up.sql`? `V1__name.sql`?), a statement splitter, and a rule for
// what a `;` inside a string literal means — three conventions the SDK would
// impose on every consumer and none of them asked for (ADR 0055 §D8).
//
// Each statement is sent as its own Exec rather than as one semicolon-joined
// string, because multi-statement execution is a per-driver capability that
// several disable by default, and because a failure then names the statement
// that failed by its position in the slice.
//
// The text is the caller's own and is never bound: a migration's DDL cannot be
// parameterised on any engine. That is exactly why migrations must not be
// built from user input, and why this helper takes a variadic of literals
// rather than a template.
func Statements(stmts ...string) coresql.Step {
	//: capture the caller's statements once; the returned Step is reusable.
	return func(ctx context.Context, ex coresql.Executor) error {
		//: in order, stopping at the first failure — a migration is a
		//: sequence, not a set.
		for _, stmt := range stmts {
			//: the error travels verbatim; the runner adds version and
			//: direction fields around it.
			if _, err := ex.ExecContext(ctx, stmt); err != nil {
				//: the surrounding transaction rolls back.
				return err
			}
		}
		//: every statement applied.
		return nil
	}
}

// ChecksumOf is the fingerprint [coresql.MigrationValue].Checksum records for
// a migration built from stmts: SHA-256 over the statements in order, each
// preceded by its length in bytes as an 8-byte big-endian unsigned integer,
// rendered as 64 lowercase hexadecimal digits.
//
// The length prefix is what makes the fingerprint the statements' and not
// their concatenation's: "CREATE TABLE a" then "(x INT)" and
// "CREATE TABLE a(x INT)" alone are different migrations and get different
// checksums. Write it beside the steps it describes, from the same
// statements, so the two cannot drift:
//
//	ddl := []string{"CREATE TABLE accounts (id BIGINT PRIMARY KEY)"}
//	m := sql.Migration{Version: 1, Name: "accounts",
//		Up: sql.Statements(ddl...), Down: sql.Statements("DROP TABLE accounts"),
//		Checksum: sql.ChecksumOf(ddl...)}
func ChecksumOf(stmts ...string) string {
	digest := sha256.New()
	var length [8]byte
	//: each statement after its length, so two statements are never read as
	//: their concatenation.
	for _, stmt := range stmts {
		binary.BigEndian.PutUint64(length[:], uint64(len(stmt)))
		//: a hash.Hash's Write never fails.
		_, _ = digest.Write(length[:])
		_, _ = digest.Write([]byte(stmt))
	}
	//: lowercase hex, 64 digits.
	return hex.EncodeToString(digest.Sum(nil))
}
