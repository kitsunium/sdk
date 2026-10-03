// Package docstore — hands the external suite's fake SQL engine the statements
// a store renders, so the engine can recognise what it is sent. The suite's
// own assertions on statement TEXT are spelled out by hand, in
// sql_statements_external_test.go.
package docstore

import coresql "github.com/kitsunium/sdk/internal/core/data/sql"

// RenderedSQL is the statement set one SQL store sends, for the fake engine.
type RenderedSQL struct {
	// Fixed maps each statement rendered once to its role.
	Fixed map[string]string
	// InsertIndexRows renders the insertion of n index rows.
	InsertIndexRows func(n int) string
	// UniqueTaken renders the unique-key check for n keys.
	UniqueTaken func(n int, shared bool) string
	// SharedKeys renders Reindex's duplicate check over n indexes.
	SharedKeys func(n int) string
	// MarkUnique renders Reindex's constraint over n indexes.
	MarkUnique func(n int) string
	// InsertVersions renders the insertion of n version rows.
	InsertVersions func(n int) string
}

// RenderSQLForTest renders the statements of a store keeping its documents in
// table on dialect.
func RenderSQLForTest(dialect coresql.Dialect, table string) RenderedSQL {
	s := renderStatements(dialect, table)
	return RenderedSQL{
		Fixed: map[string]string{
			s.getDoc: "getDoc", s.listDocs: "listDocs", s.entries: "entries", s.entriesLimit: "entriesLimit",
			s.count: "count", s.exists: "exists", s.upsert: "upsert", s.insert: "insert", s.replace: "replace",
			s.lockDoc: "lockDoc", s.writeLocked: "writeLocked", s.deleteDoc: "deleteDoc",
			s.deleteIndex: "deleteIndex", s.clearIndex: "clearIndex", s.pageAfter: "pageAfter",
			s.lookup: "lookup", s.find: "find",
			s.claim: "claim", s.versionHead: "versionHead", s.retireHead: "retireHead", s.pruneCut: "pruneCut",
			s.pruneVersions: "pruneVersions", s.dropVersions: "dropVersions", s.dropFormers: "dropFormers",
			s.readVersions: "readVersions", s.lockFormers: "lockFormers",
		},
		InsertIndexRows: s.insertIndexRows,
		UniqueTaken:     s.uniqueTaken,
		SharedKeys:      s.sharedKeys,
		MarkUnique:      s.markUnique,
		InsertVersions:  s.insertVersions,
	}
}
