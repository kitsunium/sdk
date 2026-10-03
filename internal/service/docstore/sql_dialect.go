// Package docstore — the only place the SQL store renders SQL. Every statement
// it sends is built here, once, at OpenSQL, for its dialect and its tables,
// spelled with the vocabulary core/sql's Dialect owns: the bind markers, the
// quoting and the row lock.
package docstore

import (
	"strconv"
	"strings"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
)

// indexRowArgs is how many arguments one index row binds: its index's name,
// its key, its document's key, and whether its index is unique.
const indexRowArgs int = 4

// versionRowArgs is how many arguments one version row binds: its document's
// key, its number, its instant — seconds and nanoseconds — its metadata and its
// document.
const versionRowArgs int = 6

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
	// claim creates a document, or locks the one stored and leaves it as it
	// is, for a Put on a store that keeps versions: it answers the revision it
	// wrote and the document the row holds — PostgreSQL, SQLite — or its
	// count of affected rows — MySQL.
	claim string
	// versionHead reads the number of a document's current version, locking
	// it where the engine locks rows; no row is a document stored before its
	// store kept versions.
	versionHead string
	// retireHead gives the current version the document it holds, as it
	// becomes a former one.
	retireHead string
	// pruneCut reads the newest former version a write prunes — the one past
	// the number a store keeps — which goes with every older one.
	pruneCut string
	// pruneVersions removes a document's former versions up to a number.
	pruneVersions string
	// dropVersions removes every version of a document; dropFormers every
	// former one.
	dropVersions, dropFormers string
	// readVersions reads every version of a document with the document
	// itself, newest first; lockFormers reads its former ones, locked, for a
	// rewrite.
	readVersions, lockFormers string
	// docTable, ixTable and vsTable are the three tables' quoted names.
	docTable, ixTable, vsTable string
	// shareLock ends a read that must see the latest committed rows inside a
	// transaction that has its own snapshot; empty where a plain read does.
	shareLock string
	// dialect selects the placeholder grammar of the rendered-per-call ones.
	dialect coresql.Dialect
}

// renderStatements renders every fixed statement of the store keeping its
// documents in table, on dialect. The table name was validated at OpenSQL.
func renderStatements(dialect coresql.Dialect, table string) sqlStatements {
	docs, ix, vs := dialect.QuoteIdent(table), dialect.QuoteIdent(indexTable(table)), dialect.QuoteIdent(versionsTable(table))
	p1, p2, p3 := dialect.Placeholder(1), dialect.Placeholder(2), dialect.Placeholder(3)
	//: ends a read of version rows made inside a write: it locks them where
	//: the engine locks rows and — what matters on MySQL inside a caller's
	//: REPEATABLE READ transaction — reads the latest committed rows rather
	//: than the transaction's snapshot. SQLite needs nothing: its writer
	//: holds the database's only write lock.
	lock := dialect.ForUpdate()
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
		versionHead:  "SELECT num FROM " + vs + " WHERE doc_key = " + p1 + " AND doc IS NULL" + lock,
		retireHead:   "UPDATE " + vs + " SET doc = " + p1 + " WHERE doc_key = " + p2 + " AND num = " + p3,
		pruneCut: "SELECT num FROM " + vs + " WHERE doc_key = " + p1 + " AND doc IS NOT NULL" +
			" ORDER BY num DESC LIMIT 1 OFFSET " + p2 + lock,
		pruneVersions: "DELETE FROM " + vs + " WHERE doc_key = " + p1 + " AND doc IS NOT NULL AND num <= " + p2,
		dropVersions:  "DELETE FROM " + vs + " WHERE doc_key = " + p1,
		dropFormers:   "DELETE FROM " + vs + " WHERE doc_key = " + p1 + " AND doc IS NOT NULL",
		readVersions: "SELECT v.num, v.made_at, v.made_ns, v.meta, COALESCE(v.doc, d.doc) FROM " + docs + " d LEFT JOIN " + vs +
			" v ON v.doc_key = d.doc_key WHERE d.doc_key = " + p1 + " ORDER BY v.num DESC",
		lockFormers: "SELECT num, made_at, made_ns, meta, doc FROM " + vs + " WHERE doc_key = " + p1 + " AND doc IS NOT NULL" +
			" ORDER BY num DESC" + lock,
		claim:     claimStatement(dialect, docs),
		docTable:  docs,
		ixTable:   ix,
		vsTable:   vs,
		shareLock: shareLockClause(dialect),
		dialect:   dialect,
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
	p1, p2 := dialect.Placeholder(1), dialect.Placeholder(2)
	values := "INSERT INTO " + docs + " (doc_key, rev, doc) VALUES (" + p1 + ", 1, " + p2 + ")"
	//: MySQL and MariaDB.
	if dialect == coresql.DialectMySQL {
		//: the document bound a second time as the third argument.
		return values + " ON DUPLICATE KEY UPDATE rev = rev + 1, doc = " + dialect.Placeholder(3), values
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
		return "UPDATE " + docs + " SET rev = rev + 1 WHERE doc_key = " + dialect.Placeholder(1) + " RETURNING doc",
			"UPDATE " + docs + " SET doc = " + dialect.Placeholder(1) + " WHERE doc_key = " + dialect.Placeholder(2)
	}
	//: the row lock both engines take on a locking read.
	return "SELECT doc FROM " + docs + " WHERE doc_key = " + dialect.Placeholder(1) + dialect.ForUpdate(), replace
}

// claimStatement renders the first statement of a Put on a store that keeps
// versions: it must know the document it replaces before replacing it, and
// must not let another writer create the key between its read and its write.
// So it creates the document when the key is free, and otherwise leaves the
// stored one as it is while moving its revision — which locks the row, on
// every engine, in the same statement.
//
// PostgreSQL and SQLite answer the revision and the row's document: a
// revision of 1 is a creation, and the document is then the one just written;
// any other is the stored document, now locked. MySQL has no RETURNING: its
// count of affected rows says which — 1 for a creation, 2 for a row changed,
// since the revision always changes — and the document is read after, under
// the lock this statement took.
func claimStatement(dialect coresql.Dialect, docs string) string {
	p1, p2 := dialect.Placeholder(1), dialect.Placeholder(2)
	values := "INSERT INTO " + docs + " (doc_key, rev, doc) VALUES (" + p1 + ", 1, " + p2 + ")"
	//: MySQL and MariaDB.
	if dialect == coresql.DialectMySQL {
		//: the revision moves, the document stays.
		return values + " ON DUPLICATE KEY UPDATE rev = rev + 1"
	}
	existing := "rev"
	//: PostgreSQL names the existing row by its table.
	if dialect == coresql.DialectPostgres {
		existing = docs + ".rev"
	}
	//: the revision written, and the document the row holds.
	return values + " ON CONFLICT (doc_key) DO UPDATE SET rev = " + existing + " + 1 RETURNING rev, doc"
}

// claimReturnsRow reports whether the claim answers a row holding the
// revision and the document, rather than a count of affected rows.
func (s *sqlStatements) claimReturnsRow() bool {
	//: every engine but MySQL has RETURNING.
	return s.dialect != coresql.DialectMySQL
}

// insertVersions renders the insertion of n version rows, each binding
// versionRowArgs arguments, in one statement.
func (s *sqlStatements) insertVersions(n int) string {
	var b strings.Builder
	b.WriteString("INSERT INTO " + s.vsTable + " (doc_key, num, made_at, made_ns, meta, doc) VALUES ")
	//: one tuple per row, placeholders numbered across the whole statement.
	for row := range n {
		//: rows are comma-separated.
		if row > 0 {
			b.WriteString(", ")
		}
		b.WriteString("(")
		//: the row's six columns.
		for col := 1; col <= versionRowArgs; col++ {
			//: columns are comma-separated.
			if col > 1 {
				b.WriteString(", ")
			}
			b.WriteString(s.dialect.Placeholder(row*versionRowArgs + col))
		}
		b.WriteString(")")
	}
	//: n rows in one round trip.
	return b.String()
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
			b.WriteString(s.dialect.Placeholder(row*indexRowArgs + col))
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
	b.WriteString("SELECT index_name FROM " + s.ixTable + " WHERE uniq = 1 AND doc_key <> " + s.dialect.Placeholder(1) + " AND (")
	//: one equality pair per unique key.
	for pair := range n {
		//: pairs are alternatives.
		if pair > 0 {
			b.WriteString(" OR ")
		}
		b.WriteString("(index_name = " + s.dialect.Placeholder(2+2*pair) +
			" AND index_key = " + s.dialect.Placeholder(3+2*pair) + ")")
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
		marks[i] = s.dialect.Placeholder(i + 1)
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
//
// It is this store's decision rather than a core/sql Dialect method because
// what it renders is not an engine's shared lock: PostgreSQL has one, FOR
// SHARE, which this store deliberately does not send. Which engine needs a
// locking read here follows from the isolation each one defaults to, and that
// reasoning is the store's.
func shareLockClause(dialect coresql.Dialect) string {
	//: MySQL and MariaDB both spell the shared lock this way.
	if dialect == coresql.DialectMySQL {
		//: read the latest committed version, and hold it shared.
		return " LOCK IN SHARE MODE"
	}
	//: a plain read already sees it.
	return ""
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
	docs, ix := dialect.QuoteIdent(table), dialect.QuoteIdent(indexTable(table))
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

// createVersionsTableStatements renders the DDL that creates the table a
// store keeps its versions in, beside its two others (ADR 0143): one row per
// version, keyed by the document's key and the version's number. A former
// version's row holds the document it was; the current version's holds NULL,
// because its document is the documents' table's. made_at and made_ns are the
// instant — seconds since 1970 UTC and the nanoseconds within that second, two
// integers that hold any instant a time.Time does, to the nanosecond — both
// NULL when unknown; meta is the stamp's metadata as a JSON object, NULL when
// there is none. The key is binary, as in the two other tables, and the table
// has no index but its primary key: versions are read by document, never
// looked up by what they hold.
func createVersionsTableStatements(dialect coresql.Dialect, table string) []string {
	vs := dialect.QuoteIdent(versionsTable(table))
	//: the types and the table options are each engine's.
	switch dialect {
	//: MySQL and MariaDB: InnoDB, for transactions.
	case coresql.DialectMySQL:
		//: a VARBINARY key sized like the documents'.
		return []string{"CREATE TABLE IF NOT EXISTS " + vs + " (doc_key VARBINARY(" + strconv.Itoa(MaxSQLKeyLen) + ") NOT NULL," +
			" num BIGINT NOT NULL, made_at BIGINT NULL, made_ns INT NULL, meta LONGBLOB NULL, doc LONGBLOB NULL," +
			" PRIMARY KEY (doc_key, num)) ENGINE=InnoDB"}
	//: PostgreSQL: bytea.
	case coresql.DialectPostgres:
		//: the same columns, PostgreSQL's types.
		return []string{"CREATE TABLE IF NOT EXISTS " + vs + " (doc_key bytea NOT NULL, num bigint NOT NULL, made_at bigint," +
			" made_ns integer, meta bytea, doc bytea, PRIMARY KEY (doc_key, num))"}
	//: SQLite: BLOB.
	default:
		//: the same columns, SQLite's types.
		return []string{"CREATE TABLE IF NOT EXISTS " + vs + " (doc_key BLOB NOT NULL, num INTEGER NOT NULL, made_at INTEGER," +
			" made_ns INTEGER, meta BLOB, doc BLOB, PRIMARY KEY (doc_key, num))"}
	}
}

// dropVersionsTableStatements renders the reversal of
// createVersionsTableStatements, doing nothing when the table is gone.
func dropVersionsTableStatements(dialect coresql.Dialect, table string) []string {
	//: every engine spells it alike.
	return []string{"DROP TABLE IF EXISTS " + dialect.QuoteIdent(versionsTable(table))}
}

// dropTableStatements renders the reversal of createTableStatements: the index
// table first, then the documents', each doing nothing when its table is gone.
func dropTableStatements(dialect coresql.Dialect, table string) []string {
	//: every engine spells it alike.
	return []string{
		"DROP TABLE IF EXISTS " + dialect.QuoteIdent(indexTable(table)),
		"DROP TABLE IF EXISTS " + dialect.QuoteIdent(table),
	}
}
