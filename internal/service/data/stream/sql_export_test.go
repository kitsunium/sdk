// Package stream — hands the external suite's fake SQL engine the statements
// an SQL stream renders, so the engine can recognise what it is sent. The
// suite's own assertions on statement TEXT are spelled out by hand.
package stream

import coresql "github.com/kitsunium/sdk/internal/core/data/sql"

// SQLRowsPerStatementForTest is how many records one INSERT carries.
const SQLRowsPerStatementForTest = sqlRowsPerStatement

// RenderedSQL is the statement set one SQL stream sends, for the fake engine.
type RenderedSQL struct {
	// Fixed maps each statement rendered once to its role.
	Fixed map[string]string
	// InsertRows renders the insertion of n records.
	InsertRows func(n int) string
	// CompactDelete renders the deletion of n compacted offsets.
	CompactDelete func(n int) string
	// Create and Drop are what SQLMigration's Up and Down run.
	Create, Drop []string
}

// RenderSQLForTest renders the statements of a stream keeping its records in
// table on dialect.
func RenderSQLForTest(dialect coresql.Dialect, table string) RenderedSQL {
	s := renderSQLStatements(dialect, table)
	//: every fixed statement under its role.
	return RenderedSQL{
		Fixed: map[string]string{
			s.lockHead: "lockHead", s.head: "head", s.advance: "advance", s.floor: "floor", s.read: "read",
			s.bounds: "bounds", s.offsetAt: "offsetAt", s.lowest: "lowest", s.release: "release",
			s.headNext: "headNext", s.commit: "commit", s.committed: "committed", s.compactScan: "compactScan",
		},
		InsertRows:    s.insertRows,
		CompactDelete: s.compactDelete,
		Create:        createStreamTableStatements(dialect, table),
		Drop:          dropStreamTableStatements(dialect, table),
	}
}
