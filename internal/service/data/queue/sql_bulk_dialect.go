package queue

import (
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
)

// sqlInsertArgs is how many arguments one row of a multi-row INSERT binds:
// the identifier, the due instant, the enqueue instant and the payload.
const sqlInsertArgs int = 4

// sqlAckArgs is how many arguments one receipt of AckMany's statement binds:
// the identifier, the lease and the delivery count.
const sqlAckArgs int = 3

// sqlBulkStatements are the statements of the siblings that act on a whole
// queue (ADR 0054 and ADR 0151, amended 2026-11-02): rendered once, beside
// the others, and sent only by the calls that need them — a broker whose
// policy has no MaxAge and no DeadLetterRetention sends none of them from
// Receive.
type sqlBulkStatements struct {
	// stats counts the rows by state and lease, with the earliest enqueue and
	// due instants of each group: Counter.Stats in one statement.
	stats string
	// purgeQueued and purgeLeased delete the live rows without and with a
	// lease: Purger.Purge, which reports the two counts apart.
	purgeQueued, purgeLeased string
	// expire buries every live row past MaxAge that is due — waiting and
	// visible, or a lapsed lease — with MESSAGE_EXPIRED.
	expire string
	// retention deletes every dead letter that failed at or before an
	// instant.
	retention string
	// replayMany replays the oldest dead letters, up to a bound, as replay
	// replays one; purgeDeadMany deletes the oldest that failed before an
	// instant, up to a bound. MySQL spells the bound as the UPDATE's or the
	// DELETE's own ORDER BY … LIMIT, the others as a sub-select of the ids.
	replayMany, purgeDeadMany string
	// table is the table's quoted name; dialect its placeholders.
	table   string
	dialect coresql.Dialect
	// causeArgs is how many arguments a burial's cause binds.
	causeArgs int
}

// renderSQLBulkStatements renders the bulk statements for dialect over the
// quoted table, burying through causeSet and clearing a replayed row's cause
// through cleared — the strings the burial and the replay of one row use —,
// the cause binding one more argument when trail names its column.
func renderSQLBulkStatements(dialect coresql.Dialect, quoted, causeSet, cleared string, trail bool) sqlBulkStatements {
	p := dialect.Placeholder
	causeArgs := sqlCauseArgs
	//: the trail column binds one more.
	if trail {
		causeArgs++
	}
	leased := "CASE WHEN lease IS NULL THEN 0 ELSE 1 END"
	s := sqlBulkStatements{
		stats: "SELECT dead, " + leased + ", COUNT(*), MIN(enqueued_at), MIN(due) FROM " + quoted +
			" WHERE dead < 2 GROUP BY dead, " + leased,
		purgeQueued: "DELETE FROM " + quoted + " WHERE dead = 0 AND lease IS NULL",
		purgeLeased: "DELETE FROM " + quoted + " WHERE dead = 0 AND lease IS NOT NULL",
		expire: "UPDATE " + quoted + " SET " + causeSet + " WHERE dead = 0 AND enqueued_at <= " + p(causeArgs+1) +
			" AND due <= " + p(causeArgs+2),
		retention: "DELETE FROM " + quoted + " WHERE dead = 1 AND due <= " + p(1),
		table:     quoted, dialect: dialect, causeArgs: causeArgs,
	}
	replaySet := "UPDATE " + quoted + " SET dead = 0, due = " + p(1) + ", deliveries = 0, " + cleared
	//: MySQL refuses a sub-select of the table it updates, and takes the bound
	//: on the statement itself; the others take no LIMIT on an UPDATE or a
	//: DELETE, and bound the ids instead.
	if dialect == coresql.DialectMySQL {
		s.replayMany = replaySet + " WHERE dead = 1 ORDER BY id LIMIT " + p(2)
		s.purgeDeadMany = "DELETE FROM " + quoted + " WHERE dead = 1 AND due < " + p(1) + " ORDER BY id LIMIT " + p(2)
		return s
	}
	s.replayMany = replaySet + " WHERE dead = 1 AND id IN (SELECT id FROM " + quoted +
		" WHERE dead = 1 ORDER BY id LIMIT " + p(2) + ")"
	s.purgeDeadMany = "DELETE FROM " + quoted + " WHERE dead = 1 AND id IN (SELECT id FROM " + quoted +
		" WHERE dead = 1 AND due < " + p(1) + " ORDER BY id LIMIT " + p(2) + ")"
	return s
}

// insertRows renders the INSERT of n messages, published as insert publishes
// one: n rows of (identifier, due, enqueue instant, payload).
func (s *sqlBulkStatements) insertRows(n int) string {
	rows := make([]byte, 0, n*32)
	//: one VALUES tuple per message, its four placeholders in order.
	for row := range n {
		//: a comma between tuples.
		if row > 0 {
			rows = append(rows, ", "...)
		}
		first := row*sqlInsertArgs + 1
		rows = append(rows, "("+s.dialect.Placeholder(first)+", 0, "+s.dialect.Placeholder(first+1)+", "+
			s.dialect.Placeholder(first+2)+", 0, "+s.dialect.Placeholder(first+3)+")"...)
	}
	return "INSERT INTO " + s.table + " (id, dead, due, enqueued_at, deliveries, payload) VALUES " + string(rows)
}

// ackHeld renders the condition AckMany's statements share: a live row whose
// lease is still ahead of the first argument and that one of n receipts —
// identifier, lease, count — holds.
func (s *sqlBulkStatements) ackHeld(n int) string {
	p := s.dialect.Placeholder
	held := make([]byte, 0, n*48)
	//: one (identifier, lease, count) match per receipt.
	for receipt := range n {
		//: OR between the receipts.
		if receipt > 0 {
			held = append(held, " OR "...)
		}
		first := receipt*sqlAckArgs + 2
		held = append(held, "(id = "+p(first)+" AND lease = "+p(first+1)+" AND deliveries = "+p(first+2)+")"...)
	}
	return " WHERE dead = 0 AND due > " + p(1) + " AND (" + string(held) + ")"
}

// ackRows renders AckMany's DELETE of n receipts' rows. On PostgreSQL and
// SQLite it returns the identifier and lease of each row it deleted, so the
// one statement says which receipts landed; on MySQL, which has no
// RETURNING, ackPick reads them first in the same transaction.
func (s *sqlBulkStatements) ackRows(n int) string {
	statement := "DELETE FROM " + s.table + s.ackHeld(n)
	//: MySQL reads them with ackPick instead.
	if s.dialect == coresql.DialectMySQL {
		return statement
	}
	//: the rows that landed, named by the statement that removed them.
	return statement + " RETURNING id, lease"
}

// ackPick renders MySQL's read of the rows AckMany's DELETE will remove,
// locked, in the transaction that removes them.
func (s *sqlBulkStatements) ackPick(n int) string {
	//: the same condition, read first.
	return "SELECT id, lease FROM " + s.table + s.ackHeld(n) + " FOR UPDATE"
}

// bulkRowsPerStatement bounds the rows one multi-row statement binds:
// sqlRowsPerStatement, at most, and fewer when a row binds more arguments, so
// no statement binds more than the leases' largest — well under SQLite's
// and PostgreSQL's limits.
func bulkRowsPerStatement(argsPerRow int) int {
	//: the lease's bound, in arguments, spread over the row's.
	return max(1, (sqlRowsPerStatement+2)/argsPerRow)
}
