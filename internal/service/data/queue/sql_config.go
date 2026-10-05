package queue

import (
	"regexp"
	"strconv"
	"strings"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// MaxSQLTableLen is the longest table name [NewSQL] and [SQLMigration] accept,
// in bytes: PostgreSQL's identifier limit. The broker derives no table from
// it, so the whole limit is the caller's.
const MaxSQLTableLen int = 63

// sqlTableSeparator is the run of underscores docstore's SQL engine appends
// to a table name to derive its own tables. A queue's table holds none, so it
// can never be one of a document store's in the same database.
const sqlTableSeparator string = "___"

// sqlReservedPrefix starts every name SQLite keeps for itself: creating a
// table under it is an error there, so it is refused on every engine and a
// queue moves between them unchanged.
const sqlReservedPrefix string = "sqlite_"

// sqlTablePattern is the only shape a table name may take. It is
// interpolated, quoted, into every statement — an identifier cannot be a
// bound parameter — so this pattern is the whole defence against an
// injection. Lower case, because PostgreSQL folds an unquoted name to it and
// MySQL's case sensitivity follows the server's filesystem.
var sqlTablePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// SQLConfig configures [NewSQL]: the database the queue lives in, its table,
// which clock stamps its instants, and what delivery discipline it enforces.
// Transactor, Dialect and Table are required, and Policy's zero value is
// refused as it is for every broker.
type SQLConfig struct {
	// Transactor is the transaction manager of the database the queue lives
	// in — the one the caller's own transactions are opened with, so a call
	// made under one of them joins it. It must also be a core/data/sql Joiner and
	// Deferrer, which the SDK's own transactor is: the first is how a
	// publication joins the caller's transaction, the second how it wakes a
	// consumer only once that transaction has committed.
	Transactor coresql.Transactor
	// Clock stamps every instant the broker writes — a message's visibility,
	// a lease's deadline, a dead letter's failure — and reads the instant a
	// lease is judged against. Nil means clock.System.
	//
	// The instants are the BROKER's, never the database's NOW(): a test
	// drives every deadline with a ManualClock, as it does the other two
	// brokers'. Brokers in several processes over one table compare
	// instants their own clocks wrote, so those clocks must agree to within
	// the precision a lease needs — as they must for the file broker.
	Clock clock.Clock
	// Table names the queue's one table, which [SQLMigration] creates. It is
	// a lower-case SQL identifier of at most [MaxSQLTableLen] bytes, holds no
	// three underscores in a row — docstore's derived tables do — and does
	// not start with "sqlite_".
	//
	// Two brokers over one database and one Table — in one process or in
	// twenty — are ONE queue, as two file brokers over one directory are.
	Table string
	// Policy is the delivery discipline. Its zero value is refused; see
	// core/data/queue.PolicyValue.
	Policy corequeue.PolicyValue
	// Dialect is the engine's, and must be the Transactor's own: the SQL the
	// broker speaks to it.
	Dialect coresql.Dialect
}

// sqlParts are the two capabilities the SQL broker needs of its transactor,
// found once at NewSQL.
type sqlParts struct {
	// join says where a statement issued under a context runs.
	join coresql.Joiner
	// hold keeps a wake until the transaction a publication joined commits.
	hold coresql.Deferrer
}

// validate refuses a configuration no SQL broker could run, before any
// statement is sent, and returns what the broker needs of its transactor.
func (c *SQLConfig) validate() (sqlParts, error) {
	//: the shared guard, so every broker refuses identical policies alike.
	if invalid := c.Policy.Validate(); invalid != nil {
		//: QueueMisconfigured, naming the field.
		return sqlParts{}, invalid
	}
	//: nothing to write through.
	if c.Transactor == nil {
		//: SQLQueueMisconfigured, naming the setting.
		return sqlParts{}, sqlMisconfigured("Transactor", "nil")
	}
	joiner, joins := c.Transactor.(coresql.Joiner)
	deferrer, defers := c.Transactor.(coresql.Deferrer)
	//: without both, a publication inside the caller's transaction would run
	//: outside it, or wake a consumer for a message that may be rolled back.
	if !joins || !defers {
		//: SQLQueueMisconfigured, naming what is missing.
		return sqlParts{}, sqlMisconfigured("Transactor", "not a core/data/sql Joiner and Deferrer")
	}
	//: a dialect the broker can spell.
	if !c.Dialect.Valid() {
		//: SQLQueueMisconfigured, naming the dialect.
		return sqlParts{}, sqlMisconfigured("Dialect", "not one the SDK speaks: "+c.Dialect.String())
	}
	//: a name that is safe to interpolate, and that fits.
	if tableErr := validateSQLTable(c.Table); tableErr != nil {
		//: SQLQueueMisconfigured, naming the problem.
		return sqlParts{}, tableErr
	}
	//: a broker that can be built.
	return sqlParts{join: joiner, hold: deferrer}, nil
}

// validateSQLTable refuses a table name the broker could not interpolate
// safely, or that a document store's derived table could collide with.
func validateSQLTable(table string) error {
	var problem string
	//: the four rules, the injection defence first.
	switch {
	//: the shape, which is what makes interpolating it safe.
	case !sqlTablePattern.MatchString(table):
		problem = "not a lower-case SQL identifier"
	//: longer than PostgreSQL keeps, which would truncate it into another.
	case len(table) > MaxSQLTableLen:
		problem = "longer than " + strconv.Itoa(MaxSQLTableLen) + " bytes"
	//: the separator of docstore's derived tables, which this one could be.
	case strings.Contains(table, sqlTableSeparator):
		problem = "holds three underscores in a row"
	//: SQLite's own names, refused on every engine alike.
	case strings.HasPrefix(table, sqlReservedPrefix):
		problem = "starts with sqlite_, which SQLite reserves"
	default:
		//: a table name every engine takes.
		return nil
	}
	//: SQLQueueMisconfigured; the name is the caller's own configuration.
	return kerrs.Wrap(corequeue.SQLQueueMisconfigured, kerrs.WrapParams{},
		kerrs.String("setting", "Table"), kerrs.String("problem", problem), kerrs.String("table", table))
}

// sqlMisconfigured is SQLQueueMisconfigured naming a setting and its problem.
func sqlMisconfigured(setting, problem string) error {
	//: which setting, and what is wrong with it.
	return kerrs.Wrap(corequeue.SQLQueueMisconfigured, kerrs.WrapParams{},
		kerrs.String("setting", setting), kerrs.String("problem", problem))
}
