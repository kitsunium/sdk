package docstore

import (
	"strconv"
	"strings"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"
)

// indexRowArgs is how many arguments one index row binds: its index's name,
// its key, its document's key, and whether its index is unique.
const indexRowArgs int = 4

// versionRowArgs is how many arguments one version row binds: its document's
// key, its number, its instant — seconds and nanoseconds — its metadata and its
// document.
const versionRowArgs int = 6

// trashIndexExistsStatement asks MySQL whether the versions table vs holds the
// index named name: MySQL has no IF NOT EXISTS for an index, so the trash
// index's migration asks before it adds or drops one. Both names are bound.
const trashIndexExistsStatement string = "SELECT COUNT(*) FROM information_schema.statistics" +
	" WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?"

// The columns data/sql's InsertRows inserts the store's rows into (#837).
var (
	// indexColumns are the columns an index row binds, in the order rowArgs
	// binds them — indexRowArgs of them.
	indexColumns = []string{"index_name", "index_key", "doc_key", "uniq"}
	// versionColumns are the columns a version row binds, in the order
	// versionArgs binds them — versionRowArgs of them.
	versionColumns = []string{"doc_key", "num", "made_at", "made_ns", "meta", "doc"}
)

// sqlStatements are the statements one SQL store sends, rendered for its
// dialect and tables. The fixed ones are rendered at OpenSQL; the unique-key
// check, whose length depends on a document, is rendered by the methods
// below, from the same parts; the index and version rows are inserted by
// data/sql's InsertRows over indexColumns and versionColumns (#837).
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
	// revision reads a document and the row's revision, for Revision;
	// replaceIf replaces a document while its row's revision is the one
	// bound, and lockDocIf reads and locks it on that condition — ReplaceIf's
	// two shapes, without and with versions (ADR 0209).
	revision, replaceIf, lockDocIf string
	// deleteDoc removes a document; deleteIndex removes its index rows.
	deleteDoc, deleteIndex string
	// clearIndex removes every index row, for Reindex.
	clearIndex string
	// pageAfter reads the next page of documents with their keys, in key
	// order, for Reindex: the keys above the first argument, at most the
	// second argument of them.
	pageAfter string
	// keysAfter reads the next page of keys alone, in key order, for Keys:
	// the keys above the first argument, at most the second argument of them.
	keysAfter string
	// lookup reads the document one index key files; find reads every one,
	// in key order.
	lookup, find string
	// findAfter reads the next page of what find reads, with each document's
	// key: the holders above the third argument, at most the fourth of them
	// (Pager.FindAfter).
	findAfter string
	// rangeFrom reads the next page of an ordered index's entries from a key
	// on, with each one's index key, document key and document, in that
	// order; rangeBelow is the same under an upper bound. Both resume after
	// the (index key, document key) last read, bounded below on the index key
	// alone too, so the primary key's range scan starts at the cursor
	// (Ranged.Range).
	rangeFrom, rangeBelow string
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
	// firstVersion reads a document's lowest version row — its number and
	// the document it holds — locked where the engine locks rows: number 0 is
	// the trash's row (tombstone), any other the oldest version a document
	// left.
	firstVersion string
	// dropTombstone removes a document's trash row, number 0.
	dropTombstone string
	// readTrashed reads every version a trashed document kept, with the
	// trashed document as the current one's, newest first.
	readTrashed string
	// tombstones reads every trash row's key and instant, in key order.
	tombstones string
	// versionWeight reads, in one row, what the versions weigh: how many
	// former rows — a version row holding a document —, how many trash rows,
	// and the length of every document a row holds. A current version's row
	// holds NULL, which none of them counts, and the documents are binary on
	// every engine, so LENGTH counts bytes.
	versionWeight string
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
	docs, ixName, vs := dialect.QuoteIdent(table), dialect.QuoteIdent(indexTable(table)), dialect.QuoteIdent(versionsTable(table))
	p1, p2, p3 := dialect.Placeholder(1), dialect.Placeholder(2), dialect.Placeholder(3)
	//: ends a read of version rows made inside a write: it locks them where
	//: the engine locks rows and — what matters on MySQL inside a caller's
	//: REPEATABLE READ transaction — reads the latest committed rows rather
	//: than the transaction's snapshot. SQLite needs nothing: its writer
	//: holds the database's only write lock.
	lock := dialect.ForUpdate()
	join := "SELECT d.doc FROM " + ixName + " i JOIN " + docs + " d ON d.doc_key = i.doc_key" +
		" WHERE i.index_name = " + p1 + " AND i.index_key = " + p2
	stmts := sqlStatements{
		getDoc:       "SELECT doc FROM " + docs + " WHERE doc_key = " + p1,
		listDocs:     "SELECT doc FROM " + docs + " ORDER BY doc_key",
		entries:      "SELECT doc_key, doc FROM " + docs + " ORDER BY doc_key",
		entriesLimit: "SELECT doc_key, doc FROM " + docs + " ORDER BY doc_key LIMIT " + p1,
		count:        "SELECT COUNT(*) FROM " + docs,
		replace:      "UPDATE " + docs + " SET rev = rev + 1, doc = " + p1 + " WHERE doc_key = " + p2,
		deleteDoc:    "DELETE FROM " + docs + " WHERE doc_key = " + p1,
		deleteIndex:  "DELETE FROM " + ixName + " WHERE doc_key = " + p1,
		clearIndex:   "DELETE FROM " + ixName,
		pageAfter:    "SELECT doc_key, doc FROM " + docs + " WHERE doc_key > " + p1 + " ORDER BY doc_key LIMIT " + p2,
		keysAfter:    "SELECT doc_key FROM " + docs + " WHERE doc_key > " + p1 + " ORDER BY doc_key LIMIT " + p2,
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
		versionWeight: "SELECT COUNT(CASE WHEN num > 0 AND doc IS NOT NULL THEN 1 END), COUNT(CASE WHEN num = 0 THEN 1 END), " +
			"COALESCE(SUM(LENGTH(doc)), 0) FROM " + vs,
		claim:     claimStatement(dialect, docs),
		docTable:  docs,
		ixTable:   ixName,
		vsTable:   vs,
		shareLock: shareLockClause(dialect),
		dialect:   dialect,
	}
	stmts.exists = "SELECT 1 FROM " + docs + " WHERE doc_key = " + dialect.Placeholder(1) + stmts.shareLock
	stmts.upsert, stmts.insert = writeModes(dialect, docs)
	stmts.lockDoc, stmts.writeLocked = updateStatements(dialect, docs, stmts.replace)
	stmts.revision, stmts.replaceIf, stmts.lockDocIf = revisionStatements(dialect, docs)
	stmts.firstVersion, stmts.dropTombstone, stmts.readTrashed, stmts.tombstones = trashStatements(dialect, table)
	stmts.findAfter, stmts.rangeFrom, stmts.rangeBelow = pageStatements(dialect, docs, ixName)
	//: rendered once, sent many times.
	return stmts
}

// pageStatements renders FindAfter's statement and Range's two over the
// quoted tables docs and ix. FindAfter's reads the holders of one index key
// above a document key, with their keys. Range's read from a cursor on, and
// from a cursor to an upper bound. The cursor is an (index key, document
// key) pair written without a row value, which every engine reads alike: the
// index key above the cursor's, or the cursor's own with a document key
// above it — and the index key at or above the cursor's once more, alone, so
// the primary key (index_name, index_key, doc_key) answers the range from
// its first row. Every placeholder appears once, in order — MySQL's are
// positional —, so the cursor's index key is bound three times.
func pageStatements(dialect coresql.Dialect, docs, ix string) (findAfter, from, below string) {
	p := dialect.Placeholder
	join := " i JOIN " + docs + " d ON d.doc_key = i.doc_key WHERE i.index_name = " + p(1)
	head := "SELECT i.index_key, i.doc_key, d.doc FROM " + ix + join + " AND i.index_key >= " + p(2) +
		" AND (i.index_key > " + p(3) + " OR (i.index_key = " + p(4) + " AND i.doc_key > " + p(5) + "))"
	order := " ORDER BY i.index_key, i.doc_key LIMIT "
	//: rendered once, sent many times.
	return "SELECT i.doc_key, d.doc FROM " + ix + join + " AND i.index_key = " + p(2) + " AND i.doc_key > " + p(3) +
			" ORDER BY i.doc_key LIMIT " + p(4),
		head + order + p(6), head + " AND i.index_key < " + p(6) + order + p(7)
}

// trashStatements renders the four statements of the trash, all on the
// versions table, whose row numbered 0 — a number no version is given — is a
// trashed document: the lowest row of a key, read locked where the engine
// locks rows, which tells a trash row from a version row; the trash row's
// removal; the versions a trashed document kept, with it as the current
// one's document; and every trash row's key and instant, in key order.
func trashStatements(dialect coresql.Dialect, table string) (firstVersion, dropTombstone, readTrashed, tombstones string) {
	versions, key := dialect.QuoteIdent(versionsTable(table)), dialect.Placeholder(1)
	//: rendered once, sent many times.
	return "SELECT num, doc FROM " + versions + " WHERE doc_key = " + key + " ORDER BY num LIMIT 1" + dialect.ForUpdate(),
		"DELETE FROM " + versions + " WHERE doc_key = " + key + " AND num = 0",
		"SELECT v.num, v.made_at, v.made_ns, v.meta, COALESCE(v.doc, t.doc) FROM " + versions + " t LEFT JOIN " + versions +
			" v ON v.doc_key = t.doc_key AND v.num > 0 WHERE t.doc_key = " + key + " AND t.num = 0 ORDER BY v.num DESC",
		"SELECT doc_key, made_at, made_ns FROM " + versions + " WHERE num = 0 ORDER BY doc_key"
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

// revisionStatements renders ReplaceIf's statements and Revision's read. The
// revision is the row's rev, which every write moves.
//
// replaceIf is Replace's UPDATE conditioned on the revision: on a store that
// keeps no versions the comparison and the write are one statement, and no
// row affected says the condition failed — MySQL's count is exact since the
// revision always changes. lockDocIf is lockDoc on the same condition, for a
// store that keeps versions and reads what it replaces: FOR UPDATE where the
// engine locks rows, and on SQLite a write that moves the revision and
// returns the document, as lockDoc is there.
func revisionStatements(dialect coresql.Dialect, docs string) (revision, replaceIf, lockDocIf string) {
	p1, p2, p3 := dialect.Placeholder(1), dialect.Placeholder(2), dialect.Placeholder(3)
	revision = "SELECT rev, doc FROM " + docs + " WHERE doc_key = " + p1
	replaceIf = "UPDATE " + docs + " SET rev = rev + 1, doc = " + p1 + " WHERE doc_key = " + p2 + " AND rev = " + p3
	//: SQLite: lock by writing, read what the write kept.
	if dialect == coresql.DialectSQLite {
		//: the revision moved once, by the locking read.
		return revision, replaceIf, "UPDATE " + docs + " SET rev = rev + 1 WHERE doc_key = " + p1 + " AND rev = " + p2 + " RETURNING doc"
	}
	//: the row lock both engines take on a locking read.
	return revision, replaceIf, "SELECT doc FROM " + docs + " WHERE doc_key = " + p1 + " AND rev = " + p2 + dialect.ForUpdate()
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

// rowsOf cuts args, width values a row, into the rows data/sql's InsertRows
// binds: each row a window of args, capped at its width, so nothing is
// copied and no row can grow into the next.
func rowsOf(args []any, width int) [][]any {
	rows := make([][]any, 0, len(args)/width)
	//: one window per row, in order.
	for start := 0; start+width <= len(args); start += width {
		rows = append(rows, args[start:start+width:start+width])
	}
	//: the rows, sharing args.
	return rows
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

// indexInsert is the statement data/sql's InsertRows files index rows with:
// the index table, indexColumns, indexRowsPerStatement rows at most.
func (s *sqlStatements) indexInsert() svcsql.InsertSpec {
	//: bounded per statement, as the parameter ceiling asks.
	return svcsql.InsertSpec{Table: s.ixTable, Columns: indexColumns, PerStatement: indexRowsPerStatement}
}

// versionInsert is the statement data/sql's InsertRows keeps versions with:
// the versions table, versionColumns, versionRowsPerStatement rows at most.
func (s *sqlStatements) versionInsert() svcsql.InsertSpec {
	//: bounded per statement, as the parameter ceiling asks.
	return svcsql.InsertSpec{Table: s.vsTable, Columns: versionColumns, PerStatement: versionRowsPerStatement}
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
// It is this store's decision rather than a core/data/sql Dialect method because
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
// has no index but its primary key: versions are read by document. The trash
// is the one thing looked up by what it holds — its rows numbered 0 — and
// its index is a migration of its own, SQLVersionsTrashIndexMigration, which a
// table created before it existed needs as much as a new one.
//
// The trash lives in the same table, so it needs no migration of its own: a
// trashed document is a row numbered 0 — a number no version is given — that
// holds the document and, in made_at and made_ns, when it was trashed, beside
// the version rows it kept.
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

// trashIndexStatements renders the DDL that adds and drops the index of the
// trash — the rows numbered 0 of the versions table, which Tombstones reads in
// key order. PostgreSQL and SQLite index them alone, a partial index over
// doc_key; MySQL has no partial index and keys (num, doc_key), whose prefix
// num = 0 is the trash in key order. PostgreSQL and SQLite spell both
// directions idempotently; MySQL's are guarded by trashIndexExistsStatement.
func trashIndexStatements(dialect coresql.Dialect, table string) (create, drop string) {
	vs, name := dialect.QuoteIdent(versionsTable(table)), dialect.QuoteIdent(trashIndex(table))
	//: MySQL and MariaDB: a key of the table.
	if dialect == coresql.DialectMySQL {
		//: added and dropped by ALTER TABLE.
		return "ALTER TABLE " + vs + " ADD KEY " + name + " (num, doc_key)", "ALTER TABLE " + vs + " DROP KEY " + name
	}
	//: PostgreSQL and SQLite: a partial index, in the table's schema.
	return "CREATE INDEX IF NOT EXISTS " + name + " ON " + vs + " (doc_key) WHERE num = 0", "DROP INDEX IF EXISTS " + name
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
