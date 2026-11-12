package stream

import (
	"strings"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"
)

// sqlRecordArgs is how many arguments one record binds in an insertion: its
// partition, offset, instant, key and payload.
const sqlRecordArgs int = 5

// sqlRowsPerStatement bounds how many records one INSERT carries: with five
// arguments each, 199 rows bind 995 parameters, under the 999 an SQLite older
// than 3.32 accepts, so a large batch is several statements rather than one
// the engine refuses.
const sqlRowsPerStatement int = 199

// sqlOffsetsPerDelete bounds how many offsets one DELETE of a trimmed or
// expired prefix covers, so a long prefix is released by several short
// statements rather than one that holds its locks for as long as it runs.
const sqlOffsetsPerDelete int64 = 1024

// sqlCompactPerDelete bounds how many offsets one DELETE of a compaction
// names, and sqlCompactPage how many rows one of its reads returns, so a
// compaction is many short statements rather than one long one.
const (
	sqlCompactPerDelete int = 256
	sqlCompactPage      int = 1024
)

// sqlMigrationPrefix starts the name of every migration SQLMigration builds,
// so a version table read during an incident says what created the tables.
const sqlMigrationPrefix string = "stream "

// sqlStatements are the statements one SQL stream sends, rendered once for
// its dialect and tables at NewSQL; only an INSERT shorter than a full chunk
// is rendered per call.
//
// The records' table keeps one row per record, keyed (part, record_offset).
// The heads table keeps one row per partition that was ever written: the
// next offset, the floor below which Trim made every record unreadable, and
// the instant of the last batch — the row an Append locks, which serialises
// the appenders of one partition so its offsets commit in order. The cursors
// table keeps one row per group and partition, the group keyed by its
// SHA-256 so no group name is too long for a key.
type sqlStatements struct {
	// lockHead creates the partition's head row when it is missing and
	// locks it in every case: an upsert whose update writes the row back as
	// it is. It is the Append's and the Trim's first statement, so on
	// SQLite it is also the write that takes the database's write lock
	// before anything is read (ADR 0140).
	lockHead string
	// head reads the locked head row: next offset, floor, last instant.
	head string
	// insertFull inserts a full chunk of sqlRowsPerStatement records.
	insertFull string
	// compactFull deletes a full chunk of sqlCompactPerDelete offsets a
	// compaction dropped.
	compactFull string
	// advance moves the head past a batch and records its instant.
	advance string
	// floor raises the head's floor to a Trim's offset.
	floor string
	// read reads the head — next offset, floor, and the highest stored
	// offset past the age bound — and up to a bound of records from an
	// offset, one unlocked statement: the head row LEFT JOINed with the
	// readable records, so a partition with no record still answers.
	read string
	// bounds reads the head's next offset, its floor and the highest stored
	// offset past the age bound: the first readable offset is the floor or
	// the one after that, whichever is higher, whatever holes a compaction
	// left.
	bounds string
	// offsetAt reads the head's next offset and the lowest readable record
	// appended at or after an instant.
	offsetAt string
	// lowest reads the lowest offset a partition still stores.
	lowest string
	// release deletes a partition's records below an offset.
	release string
	// headNext reads a partition's next offset, unlocked.
	headNext string
	// compactScan reads a page of the readable records old enough to be
	// compacted, from an offset on: offset, key, instant and whether the
	// payload is empty.
	compactScan string
	// commit writes a group's cursor, replacing the one it had.
	commit string
	// committed reads a group's cursor.
	committed string
	// records is the records' quoted table.
	records string
	// dialect selects the placeholder grammar of the per-call INSERT.
	dialect coresql.Dialect
}

// renderSQLStatements renders every fixed statement of the stream keeping its
// records in table on dialect. The table name was validated at NewSQL.
func renderSQLStatements(dialect coresql.Dialect, table string) sqlStatements {
	p := dialect.Placeholder
	t := dialect.QuoteIdent(table)
	h := dialect.QuoteIdent(table + sqlHeadsSuffix)
	c := dialect.QuoteIdent(table + sqlCursorsSuffix)
	readable := "r.part = h.part AND r.record_offset >= h.first_offset AND r.appended_at > "
	expired := func(n int) string {
		//: the highest stored offset at or above the floor past the age bound.
		return "(SELECT MAX(x.record_offset) FROM " + t + " x WHERE x.part = h.part AND x.record_offset >= h.first_offset" +
			" AND x.appended_at <= " + p(n) + ")"
	}
	s := sqlStatements{
		lockHead: lockHeadStatement(dialect, h),
		head:     "SELECT next_offset, first_offset, last_at FROM " + h + " WHERE part = " + p(1) + dialect.ForUpdate(),
		advance:  "UPDATE " + h + " SET next_offset = " + p(1) + ", last_at = " + p(2) + " WHERE part = " + p(3),
		floor:    "UPDATE " + h + " SET first_offset = " + p(1) + " WHERE part = " + p(2),
		read: "SELECT h.next_offset, h.first_offset, " + expired(1) + ", r.record_offset, r.appended_at, r.record_key, r.payload FROM " +
			h + " h LEFT JOIN " + t + " r ON " + readable + p(2) + " AND r.record_offset >= " + p(3) + " WHERE h.part = " + p(4) +
			" ORDER BY r.record_offset LIMIT " + p(5),
		bounds: "SELECT h.next_offset, h.first_offset, " + expired(1) + " FROM " + h + " h WHERE h.part = " + p(2),
		compactScan: "SELECT record_offset, record_key, appended_at, CASE WHEN payload IS NULL THEN 1 ELSE 0 END FROM " + t +
			" WHERE part = " + p(1) + " AND record_offset >= " + p(2) + " AND appended_at > " + p(3) + " AND appended_at <= " + p(4) +
			" ORDER BY record_offset LIMIT " + p(5),
		offsetAt: "SELECT h.next_offset, (SELECT MIN(r.record_offset) FROM " + t + " r WHERE " + readable + p(1) +
			" AND r.appended_at >= " + p(2) + ") FROM " + h + " h WHERE h.part = " + p(3),
		lowest:    "SELECT MIN(record_offset) FROM " + t + " WHERE part = " + p(1),
		release:   "DELETE FROM " + t + " WHERE part = " + p(1) + " AND record_offset < " + p(2),
		headNext:  "SELECT next_offset FROM " + h + " WHERE part = " + p(1),
		commit:    commitStatement(dialect, c),
		committed: "SELECT next_offset FROM " + c + " WHERE grp = " + p(1) + " AND part = " + p(2),
		records:   t,
		dialect:   dialect,
	}
	s.insertFull = s.insertRows(sqlRowsPerStatement)
	s.compactFull = s.compactDelete(sqlCompactPerDelete)
	//: rendered once, sent many times.
	return s
}

// lockHeadStatement renders the upsert that creates a partition's head row
// at offset zero when it is missing, and otherwise locks it by writing it
// back unchanged — the row lock that serialises a partition's appenders on
// PostgreSQL and MySQL, and the first write of the transaction on SQLite.
// PostgreSQL wants the existing row's column qualified by its table, SQLite
// names it bare, MySQL has no ON CONFLICT.
func lockHeadStatement(dialect coresql.Dialect, h string) string {
	insert := "INSERT INTO " + h + " (part, next_offset, first_offset, last_at) VALUES (" + dialect.Placeholder(1) + ", 0, 0, 0)"
	//: each engine's upsert.
	switch dialect {
	//: MySQL and MariaDB.
	case coresql.DialectMySQL:
		//: an exclusive lock on the existing row.
		return insert + " ON DUPLICATE KEY UPDATE next_offset = next_offset"
	//: PostgreSQL: the existing row named by its table.
	case coresql.DialectPostgres:
		//: the conflicting row locked by its update.
		return insert + " ON CONFLICT (part) DO UPDATE SET next_offset = " + h + ".next_offset"
	default:
		//: SQLite: a bare column is the existing row's.
		return insert + " ON CONFLICT (part) DO UPDATE SET next_offset = next_offset"
	}
}

// commitStatement renders the upsert of a group's cursor: the digest of the
// group, the partition, the offset, the group's name for an operator reading
// the table, and the offset again for the update — bound twice rather than
// read back from the insertion, which MySQL spells only with a deprecated
// VALUES().
func commitStatement(dialect coresql.Dialect, c string) string {
	p := dialect.Placeholder
	insert := "INSERT INTO " + c + " (grp, part, next_offset, grp_name) VALUES (" + p(1) + ", " + p(2) + ", " + p(3) + ", " + p(4) + ")"
	//: MySQL and MariaDB.
	if dialect == coresql.DialectMySQL {
		//: no ON CONFLICT.
		return insert + " ON DUPLICATE KEY UPDATE next_offset = " + p(5)
	}
	//: PostgreSQL and SQLite.
	return insert + " ON CONFLICT (grp, part) DO UPDATE SET next_offset = " + p(5)
}

// insertRows renders the insertion of n records, five placeholders each.
func (s *sqlStatements) insertRows(n int) string {
	var b strings.Builder
	b.WriteString("INSERT INTO " + s.records + " (part, record_offset, appended_at, record_key, payload) VALUES ")
	//: one tuple per record, numbered on from the previous one.
	for i := range n {
		//: a comma between tuples.
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('(')
		//: the record's five columns.
		for j := range sqlRecordArgs {
			//: a comma between columns.
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString(s.dialect.Placeholder(i*sqlRecordArgs + j + 1))
		}
		b.WriteByte(')')
	}
	//: one statement.
	return b.String()
}

// compactDelete renders the deletion of n offsets a compaction dropped, each
// still readable when the statement runs: a record that aged out meanwhile
// is left to the age sweep, which raises the floor before it deletes, so the
// first readable offset never moves back.
func (s *sqlStatements) compactDelete(n int) string {
	p := s.dialect.Placeholder
	var b strings.Builder
	b.WriteString("DELETE FROM " + s.records + " WHERE part = " + p(1) + " AND appended_at > " + p(2) + " AND record_offset IN (")
	//: one placeholder per offset.
	for i := range n {
		//: a comma between offsets.
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p(i + 3))
	}
	b.WriteByte(')')
	//: one statement.
	return b.String()
}

// compactFor returns the deletion of n offsets: the full chunk rendered at
// NewSQL, or one rendered now for a shorter one.
func (s *sqlStatements) compactFor(n int) string {
	//: the common full chunk.
	if n == sqlCompactPerDelete {
		return s.compactFull
	}
	//: the remainder.
	return s.compactDelete(n)
}

// insertFor returns the insertion of n records: the full chunk rendered at
// NewSQL, or one rendered now for a shorter one.
func (s *sqlStatements) insertFor(n int) string {
	//: the common large chunk.
	if n == sqlRowsPerStatement {
		return s.insertFull
	}
	//: the remainder.
	return s.insertRows(n)
}

// createStreamTableStatements renders the DDL that creates a stream's three
// tables, each statement doing nothing when its table exists, so a migration
// run twice — MySQL commits DDL implicitly, so a run can stop between them —
// changes nothing the second time.
//
// The UNIQUE constraint on (part, appended_at, record_offset) can never be
// broken, since (part, record_offset) is the key: it builds the index
// OffsetAt and the age bound walk, which PostgreSQL and SQLite declare no
// other way inside CREATE TABLE. A key and a payload admit NULL, which an
// empty one is written as and read back from.
func createStreamTableStatements(dialect coresql.Dialect, table string) []string {
	t := dialect.QuoteIdent(table)
	h := dialect.QuoteIdent(table + sqlHeadsSuffix)
	c := dialect.QuoteIdent(table + sqlCursorsSuffix)
	ints, longs, blobs, digest, options := "INTEGER", "BIGINT", "BLOB", "BLOB", ""
	//: each engine's types.
	switch dialect {
	//: MySQL and MariaDB: InnoDB, for transactions and row locks.
	case coresql.DialectMySQL:
		ints, blobs, digest, options = "INT", "LONGBLOB", "VARBINARY(32)", " ENGINE=InnoDB"
	//: PostgreSQL: bytea, compared as bytes.
	case coresql.DialectPostgres:
		ints, longs, blobs, digest = "integer", "bigint", "bytea", "bytea"
	default:
		//: SQLite: INTEGER is 64 bits.
		longs = "INTEGER"
	}
	//: the records, the heads, the cursors.
	return []string{
		"CREATE TABLE IF NOT EXISTS " + t + " (part " + ints + " NOT NULL, record_offset " + longs + " NOT NULL, appended_at " + longs +
			" NOT NULL, record_key " + blobs + " NULL, payload " + blobs + " NULL, PRIMARY KEY (part, record_offset)," +
			" UNIQUE (part, appended_at, record_offset))" + options,
		"CREATE TABLE IF NOT EXISTS " + h + " (part " + ints + " NOT NULL, next_offset " + longs + " NOT NULL, first_offset " + longs +
			" NOT NULL, last_at " + longs + " NOT NULL, PRIMARY KEY (part))" + options,
		"CREATE TABLE IF NOT EXISTS " + c + " (grp " + digest + " NOT NULL, part " + ints + " NOT NULL, next_offset " + longs +
			" NOT NULL, grp_name " + blobs + " NULL, PRIMARY KEY (grp, part))" + options,
	}
}

// dropStreamTableStatements renders the reversal of
// createStreamTableStatements, doing nothing for a table already gone.
func dropStreamTableStatements(dialect coresql.Dialect, table string) []string {
	//: every engine spells it alike, in the reverse order.
	return []string{
		"DROP TABLE IF EXISTS " + dialect.QuoteIdent(table+sqlCursorsSuffix),
		"DROP TABLE IF EXISTS " + dialect.QuoteIdent(table+sqlHeadsSuffix),
		"DROP TABLE IF EXISTS " + dialect.QuoteIdent(table),
	}
}

// SQLMigration returns the migration that creates the three tables an SQL
// stream named table keeps on dialect — table, table__heads and
// table__cursors —, as a core/data/sql MigrationValue numbered version, for
// the caller's Migrator to run under the caller's version table. Its Down
// drops the three, and every record and cursor in them.
//
// The SDK numbers nothing (ADR 0055 §D12). Each statement does nothing when
// its table already exists, or is already gone, so applying it over tables a
// previous deployment made changes nothing. Its Checksum fingerprints the
// statements Up runs, which are therefore never edited: a later column
// comes as a migration of its own.
func SQLMigration(dialect coresql.Dialect, table string, version uint64) (coresql.MigrationValue, error) {
	//: the dialect and the name the statements interpolate.
	if invalid := validateSQLNames(dialect, table); invalid != nil {
		//: SQLStreamMisconfigured, naming the setting.
		return coresql.MigrationValue{}, invalid
	}
	up := createStreamTableStatements(dialect, table)
	migration := coresql.MigrationValue{
		Version:  version,
		Name:     sqlMigrationPrefix + table,
		Up:       svcsql.Statements(up...),
		Down:     svcsql.Statements(dropStreamTableStatements(dialect, table)...),
		Checksum: svcsql.ChecksumOf(up...),
	}
	//: a version the version table can hold.
	if err := migration.Validate(); err != nil {
		//: InvalidMigration, naming the version.
		return coresql.MigrationValue{}, err
	}
	//: ready for the caller's Migrator.
	return migration, nil
}
