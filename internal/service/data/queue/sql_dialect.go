package queue

import (
	"strconv"
	"strings"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
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

// sqlCauseArgs is how many arguments a burial's cause binds before the trail
// and the identifiers: the instant, the reason, the cause and the code.
const sqlCauseArgs int = 4

// sqlTrailColumnLen is the width of MySQL's trail column: maxDeadTrail codes
// of at most ten decimal digits, and the commas between them.
const sqlTrailColumnLen int = maxDeadTrail*11 - 1

// sqlTrailColumn is the column SQLConfig.Trail keeps a dead letter's trail in.
const sqlTrailColumn string = "trail"

// sqlPriorityColumn is the column SQLConfig.Priority keeps a message's level
// in (ADR 0054, amended 2026-11-03).
const sqlPriorityColumn string = "priority"

// sqlPutDeadArgs is how many arguments PutDeadLetter's insertion binds
// before the optional columns': the identifier, the two instants, the count,
// the payload and the three of the cause.
const sqlPutDeadArgs int = 8

// sqlCauseColumnLen is the width of MySQL's reason and cause columns. What a
// dead letter keeps of a failure is bounded to publicMaxRunes runes, at most
// four bytes each.
const sqlCauseColumnLen int = 4 * publicMaxRunes

// sqlColumns are the optional columns a broker's statements name: each one
// an SQLConfig option, and each one absent from every statement without it.
type sqlColumns struct {
	// trail is SQLConfig.Trail.
	trail bool
	// priority is SQLConfig.Priority.
	priority bool
}

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
//
// A third state, dead = 2, is no message: it is a PublishOnce reservation
// (ADR 0183), its id the key's SHA-256 in hex — 64 bytes, no dash, so never a
// message's identifier — and its due the end of its window. The primary key is
// its unique constraint, so the table needs no migration for it, and every
// statement above names dead = 0 or dead = 1 and never reads one.
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
	// leaseReturning leases up to a bound of the due rows in ONE statement —
	// an UPDATE … FROM a locked, skipping sub-select, RETURNING what a
	// delivery needs and the instant each row was due — on PostgreSQL, for a
	// Receive outside the caller's transaction (issue #1042). Empty on MySQL,
	// which has no UPDATE … RETURNING, and on SQLite, whose RETURNING cannot
	// name the sub-select's due and so cannot keep a batch in visibility
	// order: both keep the transaction of pick and leaseRows. It skips a
	// lapsed lease with no attempt left, which buryExhausted buries.
	leaseReturning string
	// buryExhausted buries, with LEASE_EXPIRED, every lapsed lease with no
	// attempt left: what leaseReturning skips.
	buryExhausted string
	// ack, retry, bury and extend end or renew one lease, matching the
	// receipt's identifier, lease and delivery count, and a deadline still
	// ahead.
	ack, retry, bury, extend string
	// release hands one lease back as retry does, visible at once, with the
	// delivery count it moved on moved back, under the same condition.
	release string
	// deadList reads the dead letters after an identifier, in identifier
	// order — the enqueue order every broker lists them in (issue #1043).
	deadList string
	// replay and deleteDead act on one dead letter.
	replay, deleteDead string
	// putDead writes a dead letter as it is given: dead, its failure instant
	// as its due, its count and its cause.
	putDead string
	// liveList reads the live messages after an identifier, in identifier
	// order, without a lock: a copy's listing, not a lease.
	liveList string
	// putLive writes a live message as it is given: queued, visible at its
	// due, with its count.
	putLive string
	// once are PublishOnce's fixed statements.
	once sqlOnceStatements
	// bulk are the statements of the siblings that act on a whole queue.
	bulk sqlBulkStatements
	// table is the table's quoted name.
	table string
	// dialect selects the placeholder grammar of the per-call ones.
	dialect coresql.Dialect
	// insertPriority publishes a message at a level other than the normal
	// one; empty without SQLConfig.Priority.
	insertPriority string
	// trail is SQLConfig.Trail: every burial writes the trail column, the
	// dead-letter read returns it, a replay clears it.
	trail bool
	// priority is SQLConfig.Priority: the lease's read takes the highest
	// level first, a copy writes and the listings read the priority column.
	priority bool
}

// renderSQLStatements renders every fixed statement of the broker keeping its
// queue in table, on dialect; with cols.trail, the statements that write, read
// and clear a dead letter's cause name the trail column too, with
// cols.priority those that pick, copy and list a message name the priority
// column, and without them they are the statements the broker sent before
// the columns existed. The table name was validated at NewSQL.
func renderSQLStatements(dialect coresql.Dialect, table string, cols sqlColumns) sqlStatements {
	trail := cols.trail
	quoted := dialect.QuoteIdent(table)
	place := dialect.Placeholder
	causeSet := "dead = 1, due = " + place(1) + ", lease = NULL, reason = " + place(2) + ", cause = " + place(3) + ", code = " + place(4)
	heldFrom, deadColumns, cleared := sqlCauseArgs+1, "reason, cause, code", "reason = NULL, cause = NULL, code = NULL"
	putColumns, putValues := "reason, cause, code", place(6)+", "+place(7)+", "+place(8)
	//: one more column in every statement a cause goes through.
	if trail {
		causeSet += ", " + sqlTrailColumn + " = " + place(sqlCauseArgs+1)
		heldFrom, deadColumns = sqlCauseArgs+2, deadColumns+", "+sqlTrailColumn
		cleared += ", " + sqlTrailColumn + " = NULL"
		putColumns, putValues = putColumns+", "+sqlTrailColumn, putValues+", "+place(9)
	}
	stmts := sqlStatements{
		insert: "INSERT INTO " + quoted + " (id, dead, due, enqueued_at, deliveries, payload) VALUES (" +
			place(1) + ", 0, " + place(2) + ", " + place(3) + ", 0, " + place(4) + ")",
		probe: "SELECT MIN(due) FROM " + quoted + " WHERE dead = 0",
		//: the lease's read locks the rows it returns and skips those another
		//: transaction holds, so two consumers never wait on each other's
		//: messages and never take the same one; MySQL needs 8.0.1 for it and
		//: MariaDB 10.6, and an older server's refusal is QUEUE_BACKEND_FAILED.
		//: SQLite has no such clause and needs none: its writer holds the
		//: database's only write lock.
		pick: "SELECT id, enqueued_at, deliveries, lease, payload FROM " + quoted + " WHERE dead = 0 AND due <= " + place(1) +
			" ORDER BY due, id LIMIT " + place(2) + dialect.ForUpdateSkipLocked(),
		next:    "SELECT MIN(due) FROM " + quoted + " WHERE dead = 0 AND due > " + place(1),
		ack:     "DELETE FROM " + quoted + " WHERE " + sqlHeldBy(dialect, 1),
		retry:   "UPDATE " + quoted + " SET due = " + place(1) + ", lease = NULL WHERE " + sqlHeldBy(dialect, 2),
		bury:    "UPDATE " + quoted + " SET " + causeSet + " WHERE " + sqlHeldBy(dialect, heldFrom),
		extend:  "UPDATE " + quoted + " SET due = " + place(1) + ", lease = " + place(2) + " WHERE " + sqlHeldBy(dialect, 3),
		release: "UPDATE " + quoted + " SET due = " + place(1) + ", lease = NULL, deliveries = deliveries - 1 WHERE " + sqlHeldBy(dialect, 2),
		deadList: "SELECT id, enqueued_at, deliveries, due, " + deadColumns + ", payload FROM " + quoted +
			" WHERE dead = 1 AND id > " + place(1) + " ORDER BY id LIMIT " + place(2),
		replay:     "UPDATE " + quoted + " SET dead = 0, due = " + place(1) + ", deliveries = 0, " + cleared + " WHERE id = " + place(2) + " AND dead = 1",
		deleteDead: "DELETE FROM " + quoted + " WHERE id = " + place(1) + " AND dead = 1",
		table:      quoted,
		dialect:    dialect,
		trail:      trail,
		putDead: "INSERT INTO " + quoted + " (id, dead, due, enqueued_at, deliveries, payload, " + putColumns + ") VALUES (" +
			place(1) + ", 1, " + place(2) + ", " + place(3) + ", " + place(4) + ", " + place(5) + ", " + putValues + ")",
		liveList: "SELECT id, enqueued_at, deliveries, due, lease, payload FROM " + quoted + " WHERE dead = 0 AND id > " + place(1) +
			" ORDER BY id LIMIT " + place(2),
		putLive: "INSERT INTO " + quoted + " (id, dead, due, enqueued_at, deliveries, payload) VALUES (" +
			place(1) + ", 0, " + place(2) + ", " + place(3) + ", " + place(4) + ", " + place(5) + ")",
		once: renderSQLOnceStatements(dialect, quoted),
		bulk: renderSQLBulkStatements(dialect, quoted, causeSet, cleared, trail),
		buryExhausted: "UPDATE " + quoted + " SET " + causeSet + " WHERE dead = 0 AND lease IS NOT NULL AND due <= " +
			place(heldFrom) + " AND deliveries >= " + place(heldFrom+1),
	}
	//: the level, where the table keeps one.
	if cols.priority {
		stmts.withPriority(dialect, quoted)
	}
	//: SQLite's lock is the database's, taken by a transaction's first
	//: WRITE: a lease that read first could be refused busy at its update,
	//: its snapshot stale, so it writes first — with ADR 0140's statement,
	//: the one the migration runner takes the same lock with.
	if dialect == coresql.DialectSQLite {
		stmts.lock = svcsql.FileLockSQL(quoted)
	}
	//: PostgreSQL alone returns a column of the sub-select — the instant
	//: each row was due, which orders the batch as pick does. With levels the
	//: batch would need the level too: PostgreSQL then leases as the other
	//: engines do, through pick.
	if dialect == coresql.DialectPostgres && !cols.priority {
		stmts.leaseReturning = "UPDATE " + quoted + " SET due = " + place(1) + ", lease = " + place(2) +
			", deliveries = deliveries + 1 FROM (SELECT id, due FROM " + quoted +
			" WHERE dead = 0 AND due <= " + place(3) + " AND (lease IS NULL OR deliveries < " + place(4) + ")" +
			" ORDER BY due, id LIMIT " + place(5) + dialect.ForUpdateSkipLocked() + ") AS picked WHERE " + quoted + ".id = picked.id" +
			" RETURNING " + quoted + ".id, " + quoted + ".enqueued_at, " + quoted + ".deliveries, " + quoted + ".payload, picked.due"
	}
	//: rendered once, sent many times.
	return stmts
}

// withPriority renders the statements SQLConfig.Priority changes: the
// lease's read takes the highest level first — the index SQLPriorityMigration
// creates over (dead, priority, due, id) serves it —, a copy writes the
// level, the listings read it, and insertPriority publishes at a level.
func (s *sqlStatements) withPriority(dialect coresql.Dialect, quoted string) {
	p := dialect.Placeholder
	s.priority = true
	s.pick = "SELECT id, enqueued_at, deliveries, lease, payload FROM " + quoted + " WHERE dead = 0 AND due <= " + p(1) +
		" ORDER BY " + sqlPriorityColumn + " DESC, due, id LIMIT " + p(2) + dialect.ForUpdateSkipLocked()
	s.insertPriority = "INSERT INTO " + quoted + " (id, dead, due, enqueued_at, deliveries, payload, " + sqlPriorityColumn +
		") VALUES (" + p(1) + ", 0, " + p(2) + ", " + p(3) + ", 0, " + p(4) + ", " + p(5) + ")"
	s.putLive = "INSERT INTO " + quoted + " (id, dead, due, enqueued_at, deliveries, payload, " + sqlPriorityColumn +
		") VALUES (" + p(1) + ", 0, " + p(2) + ", " + p(3) + ", " + p(4) + ", " + p(5) + ", " + p(6) + ")"
	//: the column after the cause's, its placeholder after theirs — and
	//: after the trail's, with Trail.
	putColumns, putValues, last := "reason, cause, code", p(6)+", "+p(7)+", "+p(8), sqlPutDeadArgs+1
	//: the trail's column and placeholder come first.
	if s.trail {
		putColumns, putValues, last = putColumns+", "+sqlTrailColumn, putValues+", "+p(sqlPutDeadArgs+1), sqlPutDeadArgs+2
	}
	s.putDead = "INSERT INTO " + quoted + " (id, dead, due, enqueued_at, deliveries, payload, " + putColumns + ", " + sqlPriorityColumn +
		") VALUES (" + p(1) + ", 1, " + p(2) + ", " + p(3) + ", " + p(4) + ", " + p(5) + ", " + putValues + ", " + p(last) + ")"
	s.deadList = strings.Replace(s.deadList, ", payload FROM ", ", payload, "+sqlPriorityColumn+" FROM ", 1)
	s.liveList = strings.Replace(s.liveList, ", payload FROM ", ", payload, "+sqlPriorityColumn+" FROM ", 1)
}

// insertAt is the statement and arguments of a publication at level: the
// insertion every broker sends at the normal level, insertPriority with the
// level last at any other.
func (s *sqlStatements) insertAt(level corequeue.Priority, id []byte, due, enqueued int64, payload []byte) (string, []any) {
	//: the normal level's row is the one the table always held — its column,
	//: when there is one, defaults to it.
	if level == corequeue.PriorityNormal {
		return s.insert, []any{id, due, enqueued, payload}
	}
	//: the level written.
	return s.insertPriority, []any{id, due, enqueued, payload, int64(level)}
}

// sqlHeldBy renders the condition that says a receipt still holds its lease:
// the identifier, the lease's random half and the delivery count all match —
// a receipt whose count was edited matches nothing — the row is live, and its
// deadline is still ahead of the instant the call read. The four placeholders
// are numbered from first, in that order.
func sqlHeldBy(dialect coresql.Dialect, first int) string {
	p := func(n int) string { return dialect.Placeholder(first + n) }
	//: an expired lease is refused whether or not anybody reclaimed it.
	return "id = " + p(0) + " AND lease = " + p(1) + " AND deliveries = " + p(2) + " AND dead = 0 AND due > " + p(3)
}

// leaseRows renders the lease of n rows: their deadline, their lease's random
// half, and their delivery count moved on — for the rows still live.
func (s *sqlStatements) leaseRows(n int) string {
	//: the deadline and the lease, then the identifiers from the third.
	return "UPDATE " + s.table + " SET due = " + s.dialect.Placeholder(1) + ", lease = " + s.dialect.Placeholder(2) +
		", deliveries = deliveries + 1 WHERE dead = 0 AND id IN (" + s.list(3, n) + ")"
}

// buryRows renders the burial of n rows whose leases lapsed with no attempt
// left: dead, with the failure's instant and cause — and its trail, with
// SQLConfig.Trail, so no trail an earlier death left survives this one.
func (s *sqlStatements) buryRows(n int) string {
	p := s.dialect.Placeholder
	set, first := "", sqlCauseArgs+1
	//: the trail is a cause argument too.
	if s.trail {
		set, first = ", "+sqlTrailColumn+" = "+p(sqlCauseArgs+1), sqlCauseArgs+2
	}
	//: the instant and the cause, then the identifiers.
	return "UPDATE " + s.table + " SET dead = 1, due = " + p(1) + ", lease = NULL, reason = " + p(2) + ", cause = " + p(3) +
		", code = " + p(4) + set + " WHERE dead = 0 AND id IN (" + s.list(first, n) + ")"
}

// causeArgs binds a burial's instant and cause: the instant, the reason, the
// wire-safe Public half and the code, each bounded as the file broker bounds
// its record's header, then — with SQLConfig.Trail — the trail, NULL for
// none.
func (s *sqlStatements) causeArgs(now int64, cause causeValue) []any {
	args := []any{now, []byte(oneLine(cause.reason)), []byte(oneLine(cause.public)), int64(cause.code)}
	//: the statements name the column only with Trail.
	if s.trail {
		args = append(args, trailArg(cause.trail))
	}
	//: in the order the placeholders are numbered.
	return args
}

// putDeadArgs binds PutDeadLetter's insertion of dead under id: the row's
// identifier, instants, count and payload, then its cause, as given — with
// SQLConfig.Trail, its trail last.
func (s *sqlStatements) putDeadArgs(id string, dead corequeue.DeadLetterValue) []any {
	args := []any{
		[]byte(id), dead.FailedAt.UnixNano(), dead.Message.EnqueuedAt.UnixNano(), int64(dead.Deliveries), dead.Message.Payload,
		[]byte(dead.Reason), []byte(dead.Cause), int64(dead.Code),
	}
	//: the statement names the column only with Trail.
	if s.trail {
		args = append(args, trailArg(dead.Trail))
	}
	//: and the level only with Priority, last.
	if s.priority {
		args = append(args, int64(dead.Priority))
	}
	//: in the order the placeholders are numbered.
	return args
}

// trailArg binds a trail: its text, or NULL for none, as a dead letter buried
// before the column existed holds.
func trailArg(trail []uint32) []byte {
	//: no trail is NULL, never an empty string.
	if len(trail) == 0 {
		//: a nil []byte binds as NULL.
		return nil
	}
	//: the codes, comma-separated.
	return []byte(encodeTrail(trail))
}

// sqlOnceStatements are PublishOnce's fixed statements (ADR 0183), over
// reservation rows — dead = 2 — of the queue's own table.
type sqlOnceStatements struct {
	// drop removes one key's reservation once its window has passed.
	drop string
	// claim reserves a key, affecting no row when the key is still reserved.
	claim string
	// pick reads the passed reservations of any key, locked and skipping
	// those another transaction holds where the engine can.
	pick string
}

// renderSQLOnceStatements renders PublishOnce's fixed statements on dialect,
// over q, the queue's quoted table.
func renderSQLOnceStatements(dialect coresql.Dialect, q string) sqlOnceStatements {
	p := dialect.Placeholder
	//: rendered once, sent on every PublishOnce.
	return sqlOnceStatements{
		drop:  "DELETE FROM " + q + " WHERE id = " + p(1) + " AND dead = 2 AND due <= " + p(2),
		claim: onceClaimStatement(dialect, q),
		pick:  "SELECT id FROM " + q + " WHERE dead = 2 AND due <= " + p(1) + " ORDER BY due, id LIMIT " + p(2) + dialect.ForUpdateSkipLocked(),
	}
}

// onceSweepRows renders the removal of n passed reservations, picked by
// oncePick: still reservations, and still passed at the instant bound first.
func (s *sqlStatements) onceSweepRows(n int) string {
	//: the instant, then the identifiers from the second.
	return "DELETE FROM " + s.table + " WHERE dead = 2 AND due <= " + s.dialect.Placeholder(1) +
		" AND id IN (" + s.list(2, n) + ")"
}

// onceClaimStatement renders the reservation of a key: a row of state 2 whose
// due is the end of its window, inserted unless the key's row exists — then
// it affects no row and fails nothing, so a duplicate is an answer and not an
// error, which on PostgreSQL would also abort the caller's transaction.
//
// PostgreSQL and SQLite say so with ON CONFLICT DO NOTHING. MySQL has no ON
// CONFLICT, and its ON DUPLICATE KEY UPDATE counts a row it left unchanged as
// one or none depending on the connection's CLIENT_FOUND_ROWS; so it is
// INSERT IGNORE, whose one other effect — a value truncated or defaulted to
// fit with a warning — cannot occur here: every value is a 64-byte digest or
// an integer.
func onceClaimStatement(dialect coresql.Dialect, q string) string {
	p := dialect.Placeholder
	values := " INTO " + q + " (id, dead, due, enqueued_at, deliveries) VALUES (" + p(1) + ", 2, " + p(2) + ", " + p(3) + ", 0)"
	//: MySQL and MariaDB.
	if dialect == coresql.DialectMySQL {
		//: no row affected when the key is taken.
		return "INSERT IGNORE" + values
	}
	//: no row affected when the key is taken.
	return "INSERT" + values + " ON CONFLICT (id) DO NOTHING"
}

// list renders n comma-separated placeholders, numbered from first.
func (s *sqlStatements) list(first, n int) string {
	marks := make([]string, n)
	//: one per identifier.
	for i := range marks {
		marks[i] = s.dialect.Placeholder(first + i)
	}
	//: $3, $4 … or ?, ? …
	return strings.Join(marks, ", ")
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
	q := dialect.QuoteIdent(table)
	//: the types and the table options are each engine's.
	switch dialect {
	//: MySQL and MariaDB: InnoDB, for transactions and row locks.
	case coresql.DialectMySQL:
		//: VARBINARY and BLOB, sized.
		return []string{"CREATE TABLE IF NOT EXISTS " + q + " (id VARBINARY(" + strconv.Itoa(sqlIDColumnLen) + ") NOT NULL," +
			" dead TINYINT NOT NULL, due BIGINT NOT NULL, enqueued_at BIGINT NOT NULL, deliveries INT NOT NULL," +
			" lease VARBINARY(" + strconv.Itoa(sqlLeaseColumnLen) + ") NULL, payload LONGBLOB NULL," +
			" reason VARBINARY(" + strconv.Itoa(sqlCauseColumnLen) + ") NULL, cause VARBINARY(" + strconv.Itoa(sqlCauseColumnLen) + ") NULL," +
			" code BIGINT NULL, " + sqlTrailColumn + " " + trailColumnType(dialect) + ", PRIMARY KEY (id), UNIQUE KEY (dead, due, id)) ENGINE=InnoDB"}
	//: PostgreSQL: bytea, whose comparison is the bytes'.
	case coresql.DialectPostgres:
		//: the same columns, PostgreSQL's types.
		return []string{"CREATE TABLE IF NOT EXISTS " + q + " (id bytea NOT NULL PRIMARY KEY, dead smallint NOT NULL," +
			" due bigint NOT NULL, enqueued_at bigint NOT NULL, deliveries integer NOT NULL, lease bytea, payload bytea," +
			" reason bytea, cause bytea, code bigint, " + sqlTrailColumn + " " + trailColumnType(dialect) + ", UNIQUE (dead, due, id))"}
	//: SQLite: BLOB, compared byte for byte.
	default:
		//: the same columns, SQLite's types.
		return []string{"CREATE TABLE IF NOT EXISTS " + q + " (id BLOB NOT NULL PRIMARY KEY, dead INTEGER NOT NULL," +
			" due INTEGER NOT NULL, enqueued_at INTEGER NOT NULL, deliveries INTEGER NOT NULL, lease BLOB, payload BLOB," +
			" reason BLOB, cause BLOB, code INTEGER, " + sqlTrailColumn + " " + trailColumnType(dialect) + ", UNIQUE (dead, due, id))"}
	}
}

// dropQueueTableStatements renders the reversal of createQueueTableStatements,
// doing nothing when the table is gone.
func dropQueueTableStatements(dialect coresql.Dialect, table string) []string {
	//: every engine spells it alike.
	return []string{"DROP TABLE IF EXISTS " + dialect.QuoteIdent(table)}
}
