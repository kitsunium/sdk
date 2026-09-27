// Package docstore — the only place the SQL store renders SQL. Every statement
// it sends is built here, once, at OpenSQL, for its dialect and its tables.
package docstore

import (
	"strconv"
	"strings"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
)

// indexRowArgs is how many arguments one index row binds: its index's name,
// its key, its document's key, and whether its index is unique.
const indexRowArgs int = 4

// sqlStatements are the statements one SQL store sends, rendered for its
// dialect and tables. The fixed ones are rendered at OpenSQL; the two whose
// length depends on a document — its index rows, and its unique keys — are
// rendered by the methods below, from the same parts.
type sqlStatements struct {
	// getDoc reads one document.
	getDoc string
	// listDocs reads every document, in key order.
	listDocs string
	// entries reads every document with its key, in key order; entriesLimit
	// reads the first n.
	entries, entriesLimit string
	// count counts the documents.
	count string
	// exists reads whether a document is stored, with the latest committed
	// state — the question a refused insertion asks.
	exists string
	// upsert, insert and replace are the three write modes' statements.
	upsert, insert, replace string
	// lockDoc reads a document for Update and takes its lock; writeLocked
	// writes the result under that lock.
	lockDoc, writeLocked string
	// deleteDoc removes a document; deleteIndex removes its index rows.
	deleteDoc, deleteIndex string
	// clearIndex removes every index row, for Reindex.
	clearIndex string
	// pageAfter reads the next page of documents with their keys, in key
	// order, for Reindex: the keys above the first argument, at most the
	// second argument of them.
	pageAfter string
	// lookup reads the document one index key files; find reads every one,
	// in key order.
	lookup, find string
	// docTable and ixTable are the two tables' quoted names.
	docTable, ixTable string
	// shareLock ends a read that must see the latest committed rows inside a
	// transaction that has its own snapshot; empty where a plain read does.
	shareLock string
	// dialect selects the placeholder grammar of the rendered-per-call ones.
	dialect coresql.Dialect
}

// renderStatements renders every fixed statement of the store keeping its
// documents in table, on dialect. The table name was validated at OpenSQL.
func renderStatements(dialect coresql.Dialect, table string) sqlStatements {
	docs, ix := quoteIdent(dialect, table), quoteIdent(dialect, indexTable(table))
	p1, p2 := placeholder(dialect, 1), placeholder(dialect, 2)
	join := "SELECT d.doc FROM " + ix + " i JOIN " + docs + " d ON d.doc_key = i.doc_key" +
		" WHERE i.index_name = " + p1 + " AND i.index_key = " + p2
	s := sqlStatements{
		getDoc:       "SELECT doc FROM " + docs + " WHERE doc_key = " + p1,
		listDocs:     "SELECT doc FROM " + docs + " ORDER BY doc_key",
		entries:      "SELECT doc_key, doc FROM " + docs + " ORDER BY doc_key",
		entriesLimit: "SELECT doc_key, doc FROM " + docs + " ORDER BY doc_key LIMIT " + p1,
		count:        "SELECT COUNT(*) FROM " + docs,
		replace:      "UPDATE " + docs + " SET rev = rev + 1, doc = " + p1 + " WHERE doc_key = " + p2,
		deleteDoc:    "DELETE FROM " + docs + " WHERE doc_key = " + p1,
		deleteIndex:  "DELETE FROM " + ix + " WHERE doc_key = " + p1,
		clearIndex:   "DELETE FROM " + ix,
		pageAfter:    "SELECT doc_key, doc FROM " + docs + " WHERE doc_key > " + p1 + " ORDER BY doc_key LIMIT " + p2,
		lookup:       join,
		find:         join + " ORDER BY i.doc_key",
		docTable:     docs,
		ixTable:      ix,
		shareLock:    shareLockClause(dialect),
		dialect:      dialect,
	}
	s.exists = "SELECT 1 FROM " + docs + " WHERE doc_key = " + p1 + s.shareLock
	s.upsert, s.insert = writeModes(dialect, docs)
	s.lockDoc, s.writeLocked = updateStatements(dialect, docs, s.replace)
	//: rendered once, sent many times.
	return s
}

// writeModes renders the upsert and the insertion, the two statements whose
// grammar differs most between the engines.
//
// PostgreSQL and SQLite share one: INSERT … ON CONFLICT. The upsert RETURNS
// the revision it wrote, the first for a document it created; the insertion does
// NOTHING on a conflict, so a taken key is zero rows rather than an error — an
// error would abort a PostgreSQL transaction the caller may still want.
//
// MySQL has no RETURNING and no ON CONFLICT. Its upsert is ON DUPLICATE KEY
// UPDATE, which counts one affected row for a document it created and two for
// one it changed — and since the revision always moves, a document is always
// changed, whatever CLIENT_FOUND_ROWS the connection was opened with. The new
// document is bound TWICE rather than read back through VALUES(), which MySQL
// deprecated in 8.0.20, or through a row alias, which MariaDB does not parse.
// Its insertion is a plain INSERT: INSERT IGNORE would also turn a truncation
// into a warning, and a truncated key is another key.
func writeModes(dialect coresql.Dialect, docs string) (upsert, insert string) {
	p1, p2 := placeholder(dialect, 1), placeholder(dialect, 2)
	values := "INSERT INTO " + docs + " (doc_key, rev, doc) VALUES (" + p1 + ", 1, " + p2 + ")"
	//: MySQL and MariaDB.
	if dialect == coresql.DialectMySQL {
		//: the document bound a second time as the third argument.
		return values + " ON DUPLICATE KEY UPDATE rev = rev + 1, doc = " + placeholder(dialect, 3), values
	}
	//: PostgreSQL names the existing row by its table; SQLite reads an
	//: unqualified column as the existing row's.
	existing := "rev"
	//: PostgreSQL.
	if dialect == coresql.DialectPostgres {
		existing = docs + ".rev"
	}
	//: the revision written, 1 for a created document.
	return values + " ON CONFLICT (doc_key) DO UPDATE SET rev = " + existing + " + 1, doc = excluded.doc RETURNING rev",
		values + " ON CONFLICT (doc_key) DO NOTHING"
}

// updateStatements renders Update's two statements: the read that locks the
// document, and the write under that lock.
//
// PostgreSQL and MySQL lock the row they read, FOR UPDATE. SQLite has no row
// lock and no FOR UPDATE: its lock is the database's, and a transaction takes
// it at its first WRITE. So on SQLite the read IS a write — it moves the
// revision and RETURNS the document — and takes the write lock before the
// document is read, which is what keeps two Updates from reading the same
// version. The write that follows then leaves the revision alone.
func updateStatements(dialect coresql.Dialect, docs, replace string) (lockDoc, writeLocked string) {
	//: SQLite: lock by writing, read what the write kept.
	if dialect == coresql.DialectSQLite {
		//: the revision moved once, by the locking read.
		return "UPDATE " + docs + " SET rev = rev + 1 WHERE doc_key = ? RETURNING doc",
			"UPDATE " + docs + " SET doc = ? WHERE doc_key = ?"
	}
	//: the row lock both engines take on a locking read.
	return "SELECT doc FROM " + docs + " WHERE doc_key = " + placeholder(dialect, 1) + " FOR UPDATE", replace
}

// upsertArgs binds the upsert's arguments: the key and the document, and on
// MySQL the document again — see writeModes.
func (s *sqlStatements) upsertArgs(key, doc []byte) []any {
	//: MySQL binds the document twice.
	if s.dialect == coresql.DialectMySQL {
		//: key, document, document.
		return []any{key, doc, doc}
	}
	//: key, document.
	return []any{key, doc}
}

// upsertReturnsRev reports whether the upsert answers a row holding the
// revision it wrote, rather than a count of affected rows.
func (s *sqlStatements) upsertReturnsRev() bool {
	//: every engine but MySQL has RETURNING.
	return s.dialect != coresql.DialectMySQL
}

// insertConflictIsZeroRows reports whether an insertion over a taken key
// answers zero rows rather than an error.
func (s *sqlStatements) insertConflictIsZeroRows() bool {
	//: ON CONFLICT DO NOTHING, where it exists.
	return s.dialect != coresql.DialectMySQL
}

// insertIndexRows renders the insertion of n index rows, each binding
// indexRowArgs arguments, in one statement.
func (s *sqlStatements) insertIndexRows(n int) string {
	var b strings.Builder
	b.WriteString("INSERT INTO " + s.ixTable + " (index_name, index_key, doc_key, uniq) VALUES ")
	//: one tuple per row, placeholders numbered across the whole statement.
	for row := range n {
		//: rows are comma-separated.
		if row > 0 {
			b.WriteString(", ")
		}
		b.WriteString("(")
		//: the row's four columns.
		for col := 1; col <= indexRowArgs; col++ {
			//: columns are comma-separated.
			if col > 1 {
				b.WriteString(", ")
			}
			b.WriteString(placeholder(s.dialect, row*indexRowArgs+col))
		}
		b.WriteString(")")
	}
	//: n rows in one round trip.
	return b.String()
}

// uniqueTaken renders the question a write asks before it files a document's
// unique keys: which unique indexes already file one of these n keys under
// ANOTHER document? The first argument is the document's own key, then each
// key's index name and value. shared asks for the latest committed rows — see
// shareLockClause — which a check made after a refused write needs.
func (s *sqlStatements) uniqueTaken(n int, shared bool) string {
	var b strings.Builder
	b.WriteString("SELECT index_name FROM " + s.ixTable + " WHERE uniq = 1 AND doc_key <> " + placeholder(s.dialect, 1) + " AND (")
	//: one equality pair per unique key.
	for pair := range n {
		//: pairs are alternatives.
		if pair > 0 {
			b.WriteString(" OR ")
		}
		b.WriteString("(index_name = " + placeholder(s.dialect, 2+2*pair) +
			" AND index_key = " + placeholder(s.dialect, 3+2*pair) + ")")
	}
	b.WriteString(")")
	//: a check after a refusal reads past the transaction's snapshot.
	if shared {
		b.WriteString(s.shareLock)
	}
	//: rendered for this document's unique keys.
	return b.String()
}

// sharedKeys renders Reindex's question about n unique indexes, named by the
// n arguments: which of them file one key under two documents or more?
func (s *sqlStatements) sharedKeys(n int) string {
	//: grouped by key, within the unique indexes only.
	return "SELECT index_name FROM " + s.ixTable + " WHERE index_name IN (" + s.list(n) + ")" +
		" GROUP BY index_name, index_key HAVING COUNT(*) > 1"
}

// markUnique renders the statement that turns the rows of n indexes, named by
// the n arguments, into unique rows the table constrains.
func (s *sqlStatements) markUnique(n int) string {
	//: 1 on every row of the named indexes.
	return "UPDATE " + s.ixTable + " SET uniq = 1 WHERE index_name IN (" + s.list(n) + ")"
}

// list renders n comma-separated placeholders, numbered from 1.
func (s *sqlStatements) list(n int) string {
	marks := make([]string, n)
	//: one per argument.
	for i := range marks {
		marks[i] = placeholder(s.dialect, i+1)
	}
	//: $1, $2 … or ?, ? …
	return strings.Join(marks, ", ")
}

// shareLockClause ends a read that must see the latest COMMITTED rows even
// inside a transaction that keeps its own snapshot.
//
// It exists for MySQL. InnoDB's default isolation, REPEATABLE READ, answers a
// plain read from the snapshot the transaction took at its first read, so a
// row committed since — the one a refused write collided with — is invisible
// to it, while a locking read sees it. PostgreSQL's default, READ COMMITTED,
// sees it with a plain read, and a SQLite writer holds the database's only
// write lock, so there is nothing newer to see.
func shareLockClause(dialect coresql.Dialect) string {
	//: MySQL and MariaDB both spell the shared lock this way.
	if dialect == coresql.DialectMySQL {
		//: read the latest committed version, and hold it shared.
		return " LOCK IN SHARE MODE"
	}
	//: a plain read already sees it.
	return ""
}

// placeholder renders the n-th bind marker (1-based) for the dialect:
// PostgreSQL numbers its parameters, MySQL and SQLite do not.
func placeholder(dialect coresql.Dialect, n int) string {
	//: PostgreSQL's ordinal form.
	if dialect == coresql.DialectPostgres {
		//: $1, $2, … — the position is part of the marker.
		return "$" + strconv.Itoa(n)
	}
	//: MySQL and SQLite both use the positional question mark.
	return "?"
}

// quoteIdent quotes a validated identifier for the dialect, so a table name
// that is also a keyword — "order", "user" — is still a table name.
func quoteIdent(dialect coresql.Dialect, name string) string {
	//: MySQL quotes with backticks, whatever ANSI_QUOTES says.
	if dialect == coresql.DialectMySQL {
		//: `name`
		return "`" + name + "`"
	}
	//: the standard's double quotes, which PostgreSQL and SQLite share.
	return `"` + name + `"`
}

// createTableStatements renders the DDL that creates a store's two tables, in
// the order it must run, each one statement that does nothing when its table
// exists — so a run that stopped between them, as MySQL's implicit commit can
// make one stop, completes when it runs again (ADR 0139).
//
// Every key column is BINARY, so two keys are one key only when their bytes
// are: PostgreSQL's bytea, MySQL's VARBINARY, SQLite's BLOB. A text column
// would compare through a collation, and go-sql-driver/mysql's default one is
// case- and accent-insensitive. The document is the bytes the store encoded,
// never the engine's JSON type, which re-orders members and re-spells numbers.
//
// The index table's primary key serves Lookup and Find; its first UNIQUE
// constraint only builds the index a document's rows are found by — it can
// never be broken, and it is how PostgreSQL and SQLite, which declare no plain
// index inside CREATE TABLE, get one without a second statement; its second
// UNIQUE constraint is the unique indexes' guarantee, since uniq is 1 on a
// unique index's rows and NULL on the others, and NULLs never collide.
func createTableStatements(dialect coresql.Dialect, table string) []string {
	docs, ix := quoteIdent(dialect, table), quoteIdent(dialect, indexTable(table))
	//: the types and the table options are each engine's.
	switch dialect {
	//: MySQL and MariaDB: InnoDB, for transactions, named rather than assumed.
	case coresql.DialectMySQL:
		//: VARBINARY keys sized for InnoDB's index entry.
		return []string{
			"CREATE TABLE IF NOT EXISTS " + docs + " (doc_key VARBINARY(" + strconv.Itoa(MaxSQLKeyLen) + ") NOT NULL," +
				" rev BIGINT NOT NULL, doc LONGBLOB NOT NULL, PRIMARY KEY (doc_key)) ENGINE=InnoDB",
			"CREATE TABLE IF NOT EXISTS " + ix + " (index_name VARBINARY(" + strconv.Itoa(MaxSQLIndexNameLen) + ") NOT NULL," +
				" index_key VARBINARY(" + strconv.Itoa(MaxSQLKeyLen) + ") NOT NULL, doc_key VARBINARY(" + strconv.Itoa(MaxSQLKeyLen) + ") NOT NULL," +
				" uniq TINYINT NULL, PRIMARY KEY (index_name, index_key, doc_key), KEY (doc_key)," +
				" UNIQUE KEY (index_name, index_key, uniq)) ENGINE=InnoDB",
		}
	//: PostgreSQL: bytea, whose comparison is the bytes'.
	case coresql.DialectPostgres:
		//: the keys' lengths are checked by the store.
		return []string{
			"CREATE TABLE IF NOT EXISTS " + docs + " (doc_key bytea NOT NULL PRIMARY KEY, rev bigint NOT NULL, doc bytea NOT NULL)",
			"CREATE TABLE IF NOT EXISTS " + ix + " (index_name bytea NOT NULL, index_key bytea NOT NULL, doc_key bytea NOT NULL," +
				" uniq smallint, PRIMARY KEY (index_name, index_key, doc_key), UNIQUE (doc_key, index_name, index_key)," +
				" UNIQUE (index_name, index_key, uniq))",
		}
	//: SQLite: BLOB, compared byte for byte.
	default:
		//: the same shape, SQLite's types.
		return []string{
			"CREATE TABLE IF NOT EXISTS " + docs + " (doc_key BLOB NOT NULL PRIMARY KEY, rev INTEGER NOT NULL, doc BLOB NOT NULL)",
			"CREATE TABLE IF NOT EXISTS " + ix + " (index_name BLOB NOT NULL, index_key BLOB NOT NULL, doc_key BLOB NOT NULL," +
				" uniq INTEGER, PRIMARY KEY (index_name, index_key, doc_key), UNIQUE (doc_key, index_name, index_key)," +
				" UNIQUE (index_name, index_key, uniq))",
		}
	}
}

// dropTableStatements renders the reversal of createTableStatements: the index
// table first, then the documents', each doing nothing when its table is gone.
func dropTableStatements(dialect coresql.Dialect, table string) []string {
	//: every engine spells it alike.
	return []string{
		"DROP TABLE IF EXISTS " + quoteIdent(dialect, indexTable(table)),
		"DROP TABLE IF EXISTS " + quoteIdent(dialect, table),
	}
}
