// Package queue — hands the external suite's fake SQL engine the statements an
// SQL broker renders, so the engine can recognise what it is sent. The suite's
// own assertions on statement TEXT are spelled out by hand, in
// sql_statements_external_test.go.
package queue

import coresql "github.com/kitsunium/sdk/internal/core/sql"

// RenderedSQL is the statement set one SQL broker sends, for the fake engine.
type RenderedSQL struct {
	// Fixed maps each statement rendered once to its role.
	Fixed map[string]string
	// LeaseRows renders the lease of n rows.
	LeaseRows func(n int) string
	// BuryRows renders the burial of n lapsed leases.
	BuryRows func(n int) string
}

// RenderSQLForTest renders the statements of a broker keeping its queue in
// table on dialect.
func RenderSQLForTest(dialect coresql.Dialect, table string) RenderedSQL {
	s := renderSQLStatements(dialect, table)
	fixed := map[string]string{
		s.insert: "insert", s.probe: "probe", s.pick: "pick", s.next: "next",
		s.ack: "ack", s.retry: "retry", s.bury: "bury", s.extend: "extend",
		s.deadList: "deadList", s.replay: "replay", s.deleteDead: "deleteDead",
	}
	//: SQLite's lock, the one statement only one dialect sends.
	if s.lock != "" {
		fixed[s.lock] = "lock"
	}
	return RenderedSQL{Fixed: fixed, LeaseRows: s.leaseRows, BuryRows: s.buryRows}
}

// QueueTableSQLForTest renders the DDL SQLMigration's Up and Down run.
func QueueTableSQLForTest(dialect coresql.Dialect, table string) (up, down []string) {
	//: the statements, in the order the migration runs them.
	return createQueueTableStatements(dialect, table), dropQueueTableStatements(dialect, table)
}
