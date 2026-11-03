// Package docstore — hands the external suite's fake SQL engine the statements
// a store renders, so the engine can recognise what it is sent. The suite's
// own assertions on statement TEXT are spelled out by hand, in
// sql_statements_external_test.go.
package docstore

import (
	"context"
	stdsql "database/sql"
	"database/sql/driver"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"
)

// RenderedSQL is the statement set one SQL store sends, for the fake engine.
type RenderedSQL struct {
	// Fixed maps each statement rendered once to its role.
	Fixed map[string]string
	// InsertIndexRows is the statement data/sql's InsertRows sends for n
	// index rows, as the store calls it.
	InsertIndexRows func(n int) string
	// UniqueTaken renders the unique-key check for n keys.
	UniqueTaken func(n int, shared bool) string
	// SharedKeys renders Reindex's duplicate check over n indexes.
	SharedKeys func(n int) string
	// MarkUnique renders Reindex's constraint over n indexes.
	MarkUnique func(n int) string
	// InsertVersions is the statement InsertRows sends for n version rows.
	InsertVersions func(n int) string
}

// RenderSQLForTest renders the statements of a store keeping its documents in
// table on dialect.
func RenderSQLForTest(dialect coresql.Dialect, table string) RenderedSQL {
	s := renderStatements(dialect, table)
	createTrash, dropTrash := trashIndexStatements(dialect, table)
	rendered := RenderedSQL{
		Fixed: map[string]string{
			s.getDoc: "getDoc", s.listDocs: "listDocs", s.entries: "entries", s.entriesLimit: "entriesLimit",
			s.count: "count", s.exists: "exists", s.upsert: "upsert", s.insert: "insert", s.replace: "replace",
			s.lockDoc: "lockDoc", s.writeLocked: "writeLocked", s.deleteDoc: "deleteDoc",
			s.revision: "revision", s.replaceIf: "replaceIf", s.lockDocIf: "lockDocIf",
			s.deleteIndex: "deleteIndex", s.clearIndex: "clearIndex", s.pageAfter: "pageAfter", s.keysAfter: "keysAfter",
			s.lookup: "lookup", s.find: "find", s.findAfter: "findAfter",
			s.rangeFrom: "rangeFrom", s.rangeBelow: "rangeBelow",
			s.claim: "claim", s.versionHead: "versionHead", s.retireHead: "retireHead", s.pruneCut: "pruneCut",
			s.pruneVersions: "pruneVersions", s.dropVersions: "dropVersions", s.dropFormers: "dropFormers",
			s.readVersions: "readVersions", s.lockFormers: "lockFormers",
			s.firstVersion: "firstVersion", s.dropTombstone: "dropTombstone", s.readTrashed: "readTrashed",
			s.tombstones: "tombstones", s.versionWeight: "versionWeight",
		},
		InsertIndexRows: func(n int) string { return insertSent(s.dialect, s.ixTable, indexColumns, n) },
		UniqueTaken:     s.uniqueTaken,
		SharedKeys:      s.sharedKeys,
		MarkUnique:      s.markUnique,
		InsertVersions:  func(n int) string { return insertSent(s.dialect, s.vsTable, versionColumns, n) },
	}
	//: the trash index's migration, which a fake engine runs too.
	rendered.Fixed[createTrash] = "createTrashIndex"
	rendered.Fixed[dropTrash] = "dropTrashIndex"
	rendered.Fixed[trashIndexExistsStatement] = "trashIndexExists"
	return rendered
}

// insertSent returns the one statement svcsql.InsertRows sends to insert n
// rows of cols into table on dialect — what the store sends, so the fake
// engine recognises it and the suite pins its text (#837).
func insertSent(dialect coresql.Dialect, table string, cols []string, n int) string {
	rows := make([][]any, n)
	for i := range rows {
		rows[i] = make([]any, len(cols))
	}
	var sent sentStatement
	_, _ = svcsql.InsertRows(context.Background(), &sent, dialect, svcsql.InsertSpec{Table: table, Columns: cols, PerStatement: n}, rows)
	return sent.query
}

// sentStatement is an executor that keeps the last statement it is sent and
// answers it with no rows.
type sentStatement struct{ query string }

// ExecContext keeps query.
func (e *sentStatement) ExecContext(_ context.Context, query string, _ ...any) (stdsql.Result, error) {
	e.query = query
	return driver.RowsAffected(0), nil
}

// QueryContext is never called by InsertRows.
func (e *sentStatement) QueryContext(context.Context, string, ...any) (*stdsql.Rows, error) {
	return nil, driver.ErrSkip
}

// QueryRowContext is never called by InsertRows.
func (e *sentStatement) QueryRowContext(context.Context, string, ...any) *stdsql.Row {
	return nil
}
