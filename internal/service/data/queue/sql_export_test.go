// Package queue — hands the external suite's fake SQL engine the statements an
// SQL broker renders, so the engine can recognise what it is sent. The suite's
// own assertions on statement TEXT are spelled out by hand, in
// sql_statements_external_test.go.
package queue

import coresql "github.com/kitsunium/sdk/internal/core/data/sql"

// RenderedSQL is the statement set one SQL broker sends, for the fake engine.
type RenderedSQL struct {
	// Fixed maps each statement rendered once to its role.
	Fixed map[string]string
	// LeaseRows renders the lease of n rows.
	LeaseRows func(n int) string
	// BuryRows renders the burial of n lapsed leases.
	BuryRows func(n int) string
	// OnceSweepRows renders the removal of n passed reservations.
	OnceSweepRows func(n int) string
	// InsertRows renders the publication of n messages in one INSERT.
	InsertRows func(n int) string
	// AckRows renders the acknowledgement of n receipts in one DELETE, and
	// AckPick MySQL's read of them first.
	AckRows, AckPick func(n int) string
}

// RenderSQLForTest renders the statements of a broker keeping its queue in
// table on dialect, with SQLConfig.Trail set or not.
func RenderSQLForTest(dialect coresql.Dialect, table string, trail bool) RenderedSQL {
	//: no level kept.
	return RenderSQLColumnsForTest(dialect, table, trail, false)
}

// RenderSQLColumnsForTest renders the statements of a broker keeping its
// queue in table on dialect, with SQLConfig.Trail and SQLConfig.Priority set
// or not.
func RenderSQLColumnsForTest(dialect coresql.Dialect, table string, trail, priority bool) RenderedSQL {
	s := renderSQLStatements(dialect, table, sqlColumns{trail: trail, priority: priority})
	fixed := map[string]string{
		s.insert: "insert", s.probe: "probe", s.pick: "pick", s.next: "next", s.buryExhausted: "buryExhausted",
		s.ack: "ack", s.retry: "retry", s.bury: "bury", s.extend: "extend", s.release: "release",
		s.deadList: "deadList", s.replay: "replay", s.deleteDead: "deleteDead", s.putDead: "putDead",
		s.liveList: "liveList", s.putLive: "putLive",
		s.once.drop: "onceDrop", s.once.claim: "onceClaim", s.once.pick: "oncePick",
		s.bulk.stats: "stats", s.bulk.purgeQueued: "purgeQueued", s.bulk.purgeLeased: "purgeLeased",
		s.bulk.expire: "expire", s.bulk.retention: "retention",
		s.bulk.replayMany: "replayMany", s.bulk.purgeDeadMany: "purgeDeadMany",
	}
	//: SQLite's lock, the one statement only one dialect sends.
	if s.lock != "" {
		fixed[s.lock] = "lock"
	}
	//: the lease in one statement, which MySQL cannot send.
	if s.leaseReturning != "" {
		fixed[s.leaseReturning] = "leaseReturning"
	}
	//: a publication at a level, with SQLConfig.Priority.
	if s.insertPriority != "" {
		fixed[s.insertPriority] = "insertPriority"
	}
	column := renderTrailColumnStatements(dialect, table)
	fixed[column.add], fixed[column.drop] = "trailAdd", "trailDrop"
	//: PostgreSQL reads no catalogue first.
	if column.probe != "" {
		fixed[column.probe] = "trailProbe"
	}
	return RenderedSQL{
		Fixed: fixed, LeaseRows: s.leaseRows, BuryRows: s.buryRows, OnceSweepRows: s.onceSweepRows,
		InsertRows: s.bulk.insertRows, AckRows: s.bulk.ackRows, AckPick: s.bulk.ackPick,
	}
}

// TrailColumnSQLForTest renders what SQLTrailMigration sends: the catalogue
// read ("" on PostgreSQL), the column's addition and its removal.
func TrailColumnSQLForTest(dialect coresql.Dialect, table string) (probe, add, drop string) {
	s := renderTrailColumnStatements(dialect, table)
	//: in the order the migration may send them.
	return s.probe, s.add, s.drop
}

// QueueTableSQLForTest renders the DDL SQLMigration's Up and Down run.
func QueueTableSQLForTest(dialect coresql.Dialect, table string) (up, down []string) {
	//: the statements, in the order the migration runs them.
	return createQueueTableStatements(dialect, table), dropQueueTableStatements(dialect, table)
}

// PriorityColumnSQLForTest renders what SQLPriorityMigration sends: the
// catalogue read, the column's and the index's addition and their removal.
func PriorityColumnSQLForTest(dialect coresql.Dialect, table string) (probe string, add, drop []string) {
	s := renderPriorityColumnStatements(dialect, table)
	//: in the order the migration may send them.
	return s.probe, s.add, s.drop
}

// PriorityIndexNameForTest is the name of the index SQLPriorityMigration
// creates over table.
func PriorityIndexNameForTest(table string) string {
	//: shortened past MaxSQLTableLen.
	return priorityIndexName(table)
}
