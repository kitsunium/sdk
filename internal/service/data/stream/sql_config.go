package stream

import (
	"regexp"
	"strconv"
	"strings"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// sqlHeadsSuffix and sqlCursorsSuffix derive the stream's two other tables
// from its records' table.
const (
	sqlHeadsSuffix   string = "__heads"
	sqlCursorsSuffix string = "__cursors"
)

// sqlMaxTableLen is the longest Table NewSQL accepts, in bytes: PostgreSQL's
// 63-byte identifier limit less the longest suffix, so no derived name is
// ever truncated into another table's.
const sqlMaxTableLen int = 63 - len(sqlCursorsSuffix)

// sqlReservedPrefix starts every name SQLite keeps for itself: refused on
// every engine, so a stream moves between them unchanged.
const sqlReservedPrefix string = "sqlite_"

// sqlTablePattern is the only shape a Table may take: words of lower-case
// letters and digits joined by single underscores. It is interpolated, quoted,
// into every statement — an identifier cannot be a bound parameter — so this
// pattern is the whole defence against an injection; and since it holds no
// double underscore, no stream's table is ever another stream's derived one.
var sqlTablePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

// SQLConfig configures [NewSQL]: the database the stream lives in, its
// tables, the clock that stamps its records, and the policy it enforces.
// Transactor, Dialect and Table are required; Policy's zero value works.
type SQLConfig struct {
	// Transactor is the transaction manager of the database the stream
	// lives in — the one the caller's own transactions are opened with, so
	// a call made under one of them joins it. It must also be a
	// core/data/sql Joiner and Deferrer, which the SDK's own transactor
	// is: the first is how an Append or a Commit joins the caller's
	// transaction, the second how the Waker fires only once that
	// transaction has committed.
	Transactor coresql.Transactor
	// Dialect is the engine's, and must be the Transactor's own: the SQL
	// the stream speaks to it.
	Dialect coresql.Dialect
	// Table names the records' table; the heads and the cursors are kept
	// in Table__heads and Table__cursors, which [SQLMigration] creates
	// with it. It is a lower-case SQL identifier of at most 54 bytes —
	// PostgreSQL's 63 less the longest suffix —, words of letters and
	// digits joined by single underscores, starting with a letter and not
	// with "sqlite_". Two streams over one database and one Table, in one
	// process or in twenty, are ONE stream.
	Table string
	// Policy is what the stream enforces; its zero value works. Its
	// RetainBytes and MaxBytes must be zero: the engine keeps no byte
	// count. Its Partitions must stay the same for the life of the
	// tables: a key's partition is its hash modulo that number.
	Policy corestream.PolicyValue
	// Clock stamps [corestream.EntryValue].AppendedAt and ages records for
	// [corestream.PolicyValue].RetainFor. The instants are the ENGINE's,
	// never the database's NOW(), so a test drives retention with a
	// ManualClock; engines in several processes over one table compare
	// instants their own clocks wrote, and a batch is never stamped before
	// the partition's previous one. Nil means clock.System.
	Clock clock.Clock
}

// sqlParts are the two capabilities the SQL engine needs of its transactor,
// found once at NewSQL.
type sqlParts struct {
	// join says where a statement issued under a context runs.
	join coresql.Joiner
	// hold keeps a wake until the transaction an Append joined commits.
	hold coresql.Deferrer
}

// validate refuses a configuration no SQL stream could run, before any
// statement is sent, and returns what the engine needs of its transactor.
func (c *SQLConfig) validate() (sqlParts, error) {
	//: the shared guard, so every engine refuses identical policies alike.
	if invalid := c.Policy.Validate(); invalid != nil {
		//: StreamMisconfigured, naming the field.
		return sqlParts{}, invalid
	}
	//: the byte bounds need a count of every partition's bytes, which the
	//: tables do not keep: refused rather than ignored.
	if c.Policy.RetainBytes != 0 {
		//: SQLStreamMisconfigured, naming the field.
		return sqlParts{}, sqlMisconfigured("Policy.RetainBytes", "the SQL engine keeps no byte count")
	}
	//: the same for the producers' bound.
	if c.Policy.MaxBytes != 0 {
		//: SQLStreamMisconfigured, naming the field.
		return sqlParts{}, sqlMisconfigured("Policy.MaxBytes", "the SQL engine keeps no byte count")
	}
	//: nothing to write through.
	if c.Transactor == nil {
		//: SQLStreamMisconfigured, naming the setting.
		return sqlParts{}, sqlMisconfigured("Transactor", "nil")
	}
	joiner, joins := c.Transactor.(coresql.Joiner)
	deferrer, defers := c.Transactor.(coresql.Deferrer)
	//: without both, an Append inside the caller's transaction would run
	//: outside it, or wake a reader for records a rollback takes back.
	if !joins || !defers {
		//: SQLStreamMisconfigured, naming what is missing.
		return sqlParts{}, sqlMisconfigured("Transactor", "not a core/data/sql Joiner and Deferrer")
	}
	//: a dialect the engine can spell, and a table it can interpolate.
	if invalid := validateSQLNames(c.Dialect, c.Table); invalid != nil {
		//: SQLStreamMisconfigured, naming the setting.
		return sqlParts{}, invalid
	}
	//: an engine that can be built.
	return sqlParts{join: joiner, hold: deferrer}, nil
}

// validateSQLNames refuses a dialect the engine cannot spell and a table name
// it could not interpolate safely.
func validateSQLNames(dialect coresql.Dialect, table string) error {
	//: a dialect the engine can spell.
	if !dialect.Valid() {
		//: SQLStreamMisconfigured, naming the dialect.
		return sqlMisconfigured("Dialect", "not one the SDK speaks: "+dialect.String())
	}
	var problem string
	//: the three rules, the injection defence first.
	switch {
	//: the shape, which is what makes interpolating it safe.
	case !sqlTablePattern.MatchString(table):
		problem = "not lower-case words joined by single underscores"
	//: longer than PostgreSQL keeps once suffixed.
	case len(table) > sqlMaxTableLen:
		problem = "longer than " + strconv.Itoa(sqlMaxTableLen) + " bytes"
	//: SQLite's own names, refused on every engine alike.
	case strings.HasPrefix(table, sqlReservedPrefix):
		problem = "starts with sqlite_, which SQLite reserves"
	default:
		//: a table name every engine takes.
		return nil
	}
	//: SQLStreamMisconfigured; the name is the caller's own configuration.
	return errs.Wrap(corestream.SQLStreamMisconfigured, errs.WrapParams{},
		errs.String("field", "Table"), errs.String("problem", problem), errs.String("table", table))
}

// sqlMisconfigured is SQLStreamMisconfigured naming a field and its problem.
func sqlMisconfigured(field, problem string) error {
	//: which field, and what is wrong with it.
	return errs.Wrap(corestream.SQLStreamMisconfigured, errs.WrapParams{},
		errs.String("field", field), errs.String("problem", problem))
}

// sqlClockOrSystem resolves a nil clock to the wall clock.
func sqlClockOrSystem(clk clock.Clock) clock.Clock {
	//: nil is the caller who has no opinion.
	if clk == nil {
		//: the production value.
		return clock.System
	}
	//: the caller's, usually a ManualClock in a test.
	return clk
}
