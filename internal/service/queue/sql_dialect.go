// Package queue — the only place the SQL broker renders SQL. Every statement
// it sends is built here, once, at NewSQL, for its dialect and its table; the
// two whose length depends on a batch are rendered per call from the same
// parts.
package queue

import (
	"strconv"
	"strings"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
)

// sqlRowsPerStatement bounds how many identifiers one lease or burial of a
// batch names. With its four other arguments it stays under the 999 bound
// parameters of an SQLite older than 3.32, so a large batch is several
// statements rather than one the engine refuses.
const sqlRowsPerStatement int = 500

// sqlIDColumnLen is the width of MySQL's id column, in bytes. An identifier is
// a 19-digit instant, a dash and 16 hex digits — 36 bytes — and the column
// leaves room for a longer one without a migration.
const sqlIDColumnLen int = 64

// sqlLeaseColumnLen is the width of MySQL's lease column: the hex of the
// lease's random half.
const sqlLeaseColumnLen int = 32

// sqlCauseColumnLen is the width of MySQL's reason and cause columns. What a
// dead letter keeps of a failure is bounded to publicMaxRunes runes, at most
// four bytes each.
const sqlCauseColumnLen int = 4 * publicMaxRunes

// sqlStatements are the statements one SQL broker sends, rendered for its
// dialect and table.
//
// One table holds the whole queue, and a row's state is three columns: dead
// is 0 for a live message and 1 for a dead letter; lease is NULL for a queued
// message and the lease's random half for a leased one; due is the instant
// the row next matters on its own — a queued message's visibility, a leased
// one's deadline, a dead letter's failure. So every live row whose due has
// passed is either receivable or a lease whose holder may have died, and one
// ordered read finds both.
type sqlStatements struct {
	// insert publishes a message.
	insert string
	// probe reads the earliest instant any live row is due: nothing is
	// receivable before it, so an idle Receive reads only this.
	probe string
	// lock takes SQLite's write lock with a write that writes nothing, before
	// the lease's first read; empty on every other engine.
	lock string
	// pick reads the live rows due by now, oldest due first, locked and
	// skipping those another transaction holds where the engine can.
	pick string
	// next reads the earliest instant a live row becomes due after now.
	next string
	// ack, retry, bury and extend end or renew one lease, matching the
	// receipt's identifier, lease and delivery count, and a deadline still
	// ahead.
	ack, retry, bury, extend string
	// deadList reads the dead letters, in the order they died.
	deadList string
	// replay and deleteDead act on one dead letter.
	replay, deleteDead string
	// table is the table's quoted name.
	table string
	// dialect selects the placeholder grammar of the per-call ones.
	dialect coresql.Dialect
}

// renderSQLStatements renders every fixed statement of the broker keeping its
// queue in table, on dialect. The table name was validated at NewSQL.
func renderSQLStatements(dialect coresql.Dialect, table string) sqlStatements {
	q := sqlQuoteIdent(dialect, table)
	p := func(n int) string { return sqlPlaceholder(dialect, n) }
	causeSet := "dead = 1, due = " + p(1) + ", lease = NULL, reason = " + p(2) + ", cause = " + p(3) + ", code = " + p(4)
	s := sqlStatements{
		insert: "INSERT INTO " + q + " (id, dead, due, enqueued_at, deliveries, payload) VALUES (" +
			p(1) + ", 0, " + p(2) + ", " + p(3) + ", 0, " + p(4) + ")",
		probe: "SELECT MIN(due) FROM " + q + " WHERE dead = 0",
		pick: "SELECT id, enqueued_at, deliveries, lease, payload FROM " + q + " WHERE dead = 0 AND due <= " + p(1) +
			" ORDER BY due, id LIMIT " + p(2) + sqlSkipLockedClause(dialect),
		next:       "SELECT MIN(due) FROM " + q + " WHERE dead = 0 AND due > " + p(1),
		ack:        "DELETE FROM " + q + " WHERE " + sqlHeldBy(dialect, 1),
		retry:      "UPDATE " + q + " SET due = " + p(1) + ", lease = NULL WHERE " + sqlHeldBy(dialect, 2),
		bury:       "UPDATE " + q + " SET " + causeSet + " WHERE " + sqlHeldBy(dialect, 5),
		extend:     "UPDATE " + q + " SET due = " + p(1) + ", lease = " + p(2) + " WHERE " + sqlHeldBy(dialect, 3),
		deadList:   "SELECT id, enqueued_at, deliveries, due, reason, cause, code, payload FROM " + q + " WHERE dead = 1 ORDER BY due, id LIMIT " + p(1),
		replay:     "UPDATE " + q + " SET dead = 0, due = " + p(1) + ", deliveries = 0, reason = NULL, cause = NULL, code = NULL WHERE id = " + p(2) + " AND dead = 1",
		deleteDead: "DELETE FROM " + q + " WHERE id = " + p(1) + " AND dead = 1",
		table:      q,
		dialect:    dialect,
	}
	//: SQLite's lock is the database's, taken by a transaction's first
	//: WRITE: a lease that read first could be refused busy at its update,
	//: its snapshot stale, so it writes first. ADR 0140's statement.
	if dialect == coresql.DialectSQLite {
		s.lock = "DELETE FROM " + q + " WHERE 1 = 0"
	}
	//: rendered once, sent many times.
	return s
}

// sqlHeldBy renders the condition that says a receipt still holds its lease:
// the identifier, the lease's random half and the delivery count all match —
// a receipt whose count was edited matches nothing — the row is live, and its
// deadline is still ahead of the instant the call read. The four placeholders
// are numbered from first, in that order.
func sqlHeldBy(dialect coresql.Dialect, first int) string {
	p := func(n int) string { return sqlPlaceholder(dialect, first+n) }
	//: an expired lease is refused whether or not anybody reclaimed it.
	return "id = " + p(0) + " AND lease = " + p(1) + " AND deliveries = " + p(2) + " AND dead = 0 AND due > " + p(3)
}

// leaseRows renders the lease of n rows: their deadline, their lease's random
// half, and their delivery count moved on — for the rows still live.
func (s *sqlStatements) leaseRows(n int) string {
	//: the deadline and the lease, then the identifiers from the third.
	return "UPDATE " + s.table + " SET due = " + sqlPlaceholder(s.dialect, 1) + ", lease = " + sqlPlaceholder(s.dialect, 2) +
		", deliveries = deliveries + 1 WHERE dead = 0 AND id IN (" + s.list(3, n) + ")"
}

// buryRows renders the burial of n rows whose leases lapsed with no attempt
// left: dead, with the failure's instant and cause.
func (s *sqlStatements) buryRows(n int) string {
	p := func(k int) string { return sqlPlaceholder(s.dialect, k) }
	//: the instant and the cause, then the identifiers from the fifth.
	return "UPDATE " + s.table + " SET dead = 1, due = " + p(1) + ", lease = NULL, reason = " + p(2) + ", cause = " + p(3) +
		", code = " + p(4) + " WHERE dead = 0 AND id IN (" + s.list(5, n) + ")"
}

// list renders n comma-separated placeholders, numbered from first.
func (s *sqlStatements) list(first, n int) string {
	marks := make([]string, n)
	//: one per identifier.
	for i := range marks {
		marks[i] = sqlPlaceholder(s.dialect, first+i)
	}
	//: $3, $4 … or ?, ? …
	return strings.Join(marks, ", ")
}

// sqlSkipLockedClause ends the lease's read: it locks the rows it returns and
// skips those another transaction holds, so two consumers never wait on each
// other's messages and never take the same one. SQLite has no such clause and
// needs none — its writer holds the database's only write lock.
//
// MySQL has SKIP LOCKED since 8.0.1 and MariaDB since 10.6; an older server
// refuses the statement, and the caller gets QUEUE_BACKEND_FAILED.
func sqlSkipLockedClause(dialect coresql.Dialect) string {
	//: PostgreSQL, MySQL and MariaDB.
	if dialect != coresql.DialectSQLite {
		//: locked, and never waited for.
		return " FOR UPDATE SKIP LOCKED"
	}
	//: SQLite: the transaction's write lock is the exclusion.
	return ""
}

// sqlPlaceholder renders the n-th bind marker (1-based) for the dialect:
// PostgreSQL numbers its parameters, MySQL and SQLite do not.
func sqlPlaceholder(dialect coresql.Dialect, n int) string {
	//: PostgreSQL's ordinal form.
	if dialect == coresql.DialectPostgres {
		//: $1, $2, … — the position is part of the marker.
		return "$" + strconv.Itoa(n)
	}
	//: MySQL and SQLite both use the positional question mark.
	return "?"
}

// sqlQuoteIdent quotes a validated identifier for the dialect, so a table
// name that is also a keyword — "order", "user" — is still a table name.
func sqlQuoteIdent(dialect coresql.Dialect, name string) string {
	//: MySQL quotes with backticks, whatever ANSI_QUOTES says.
	if dialect == coresql.DialectMySQL {
		//: `name`
		return "`" + name + "`"
	}
	//: the standard's double quotes, which PostgreSQL and SQLite share.
	return `"` + name + `"`
}

// createQueueTableStatements renders the DDL that creates a queue's table: one
// statement that does nothing when the table exists, so a migration run twice
// — MySQL commits DDL implicitly, so a run can stop after it — changes
// nothing the second time.
//
// The identifier and the lease are BINARY, compared as bytes whatever the
// connection's collation; the payload is the bytes the producer handed over,
// and admits NULL, which a nil payload binds as — an empty payload is a
// legitimate message, and reads back as empty either way. The
// instants are 64-bit integers of Unix nanoseconds, the range a file broker's
// names carry. The UNIQUE constraint on (dead, due, id) can never be broken,
// since id is the key: it exists to build the index every lease, probe and
// dead-letter read walks in order, which PostgreSQL and SQLite declare no
// other way inside CREATE TABLE.
func createQueueTableStatements(dialect coresql.Dialect, table string) []string {
	q := sqlQuoteIdent(dialect, table)
	//: the types and the table options are each engine's.
	switch dialect {
	//: MySQL and MariaDB: InnoDB, for transactions and row locks.
	case coresql.DialectMySQL:
		//: VARBINARY and BLOB, sized.
		return []string{"CREATE TABLE IF NOT EXISTS " + q + " (id VARBINARY(" + strconv.Itoa(sqlIDColumnLen) + ") NOT NULL," +
			" dead TINYINT NOT NULL, due BIGINT NOT NULL, enqueued_at BIGINT NOT NULL, deliveries INT NOT NULL," +
			" lease VARBINARY(" + strconv.Itoa(sqlLeaseColumnLen) + ") NULL, payload LONGBLOB NULL," +
			" reason VARBINARY(" + strconv.Itoa(sqlCauseColumnLen) + ") NULL, cause VARBINARY(" + strconv.Itoa(sqlCauseColumnLen) + ") NULL," +
			" code BIGINT NULL, PRIMARY KEY (id), UNIQUE KEY (dead, due, id)) ENGINE=InnoDB"}
	//: PostgreSQL: bytea, whose comparison is the bytes'.
	case coresql.DialectPostgres:
		//: the same columns, PostgreSQL's types.
		return []string{"CREATE TABLE IF NOT EXISTS " + q + " (id bytea NOT NULL PRIMARY KEY, dead smallint NOT NULL," +
			" due bigint NOT NULL, enqueued_at bigint NOT NULL, deliveries integer NOT NULL, lease bytea, payload bytea," +
			" reason bytea, cause bytea, code bigint, UNIQUE (dead, due, id))"}
	//: SQLite: BLOB, compared byte for byte.
	default:
		//: the same columns, SQLite's types.
		return []string{"CREATE TABLE IF NOT EXISTS " + q + " (id BLOB NOT NULL PRIMARY KEY, dead INTEGER NOT NULL," +
			" due INTEGER NOT NULL, enqueued_at INTEGER NOT NULL, deliveries INTEGER NOT NULL, lease BLOB, payload BLOB," +
			" reason BLOB, cause BLOB, code INTEGER, UNIQUE (dead, due, id))"}
	}
}

// dropQueueTableStatements renders the reversal of createQueueTableStatements,
// doing nothing when the table is gone.
func dropQueueTableStatements(dialect coresql.Dialect, table string) []string {
	//: every engine spells it alike.
	return []string{"DROP TABLE IF EXISTS " + sqlQuoteIdent(dialect, table)}
}
