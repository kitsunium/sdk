package docstore

import (
	"regexp"
	"strconv"
	"strings"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// MaxSQLTableLen is the longest table name a SQL store accepts, in bytes:
// PostgreSQL's identifier limit of 63, less the five bytes of the longest names
// the store derives from it — its index table, the name followed by "___ix",
// and its versions table, followed by "___vs".
// A caller whose names can be longer shortens them, with a digest, before
// they reach the store.
const MaxSQLTableLen int = 58

// MaxSQLKeyLen is the longest store key, and the longest index key as it
// reaches the table, in bytes. An index row carries an index key and a store
// key in one primary key, and MySQL's InnoDB caps an index entry at 3072
// bytes: two keys of this length and an index name fit in it, with room left.
const MaxSQLKeyLen int = 1024

// MaxSQLIndexNameLen is the longest index name a SQL store accepts, in bytes.
// An index's name is stored in every one of its rows, beside its keys.
const MaxSQLIndexNameLen int = 64

// derivedSeparator joins a store's table name and the tag of a table the
// store derives from it. Three underscores, which a table name may not
// contain, so no derived name is ever another store's table.
const derivedSeparator string = "___"

// indexTableTag names the table of index rows beside the documents' table.
const indexTableTag string = "ix"

// versionsTableTag names the table of versions beside the documents' table
// (ADR 0143).
const versionsTableTag string = "vs"

// reservedTablePrefix starts every name SQLite keeps for itself: creating a
// table under it is an error there, so it is refused on every engine and a
// store moves between them unchanged.
const reservedTablePrefix string = "sqlite_"

// tablePattern is the only shape a table name may take. It is interpolated
// into every statement — an identifier cannot be a bound parameter — so this
// pattern is the whole defence against an injection. Lower case, because
// PostgreSQL folds an unquoted name to it and MySQL's case sensitivity follows
// the server's filesystem: a name that differs only by case would be one table
// on one engine and two on another.
var tablePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// txParts are the two capabilities a SQL store needs of its transactor, found
// once at OpenSQL.
type txParts struct {
	// join says where a statement issued under a context runs.
	join coresql.Joiner
	// hold keeps a write's hooks until its transaction commits.
	hold coresql.Deferrer
}

// validate refuses a configuration no SQL store could honour, with the indexes
// OpenSQL was given, before any statement is sent, and returns what the store
// needs of its transactor.
func (c *SQLConfig[T]) validate(indexes []coredocstore.IndexSpec[T]) (txParts, error) {
	//: a store that cannot key a document cannot store one.
	if c.Key == nil {
		//: StoreMisconfigured, naming the setting.
		return txParts{}, misconfigured("Key", "nil")
	}
	parts, err := validateTransactor(c.Transactor)
	//: the transactor, and what the store needs of it.
	if err != nil {
		//: StoreMisconfigured, naming the setting.
		return txParts{}, err
	}
	//: a dialect the store can spell.
	if !c.Dialect.Valid() {
		//: StoreMisconfigured, naming the dialect.
		return txParts{}, misconfigured("Dialect", "not one the SDK speaks: "+c.Dialect.String())
	}
	//: a name that is safe to interpolate, and that fits.
	if tableErr := validateTable(c.Table); tableErr != nil {
		//: StoreMisconfigured, naming the problem.
		return txParts{}, tableErr
	}
	//: how many versions, and a hold only where versions are kept.
	if versionsErr := validateVersions(c.Versions, c.Held != nil); versionsErr != nil {
		//: StoreMisconfigured, naming the setting.
		return txParts{}, versionsErr
	}
	//: the declarations every engine refuses alike, then what a column holds.
	if ixErr := validateSQLIndexes(indexes); ixErr != nil {
		//: StoreMisconfigured, naming the index.
		return txParts{}, ixErr
	}
	//: a store that can be opened.
	return parts, nil
}

// validateSQLIndexes refuses what every engine refuses, and an index name
// longer than the column every one of its rows stores it in.
func validateSQLIndexes[T any](indexes []coredocstore.IndexSpec[T]) error {
	//: no name, a name twice, no key function.
	if err := validateIndexes(indexes); err != nil {
		//: StoreMisconfigured, naming the index.
		return err
	}
	//: an index name is stored in every row of its index.
	for _, spec := range indexes {
		//: longer than the column.
		if len(spec.Name) > MaxSQLIndexNameLen {
			//: StoreMisconfigured, naming the index and the limit.
			return kerrs.Wrap(coredocstore.StoreMisconfigured, kerrs.WrapParams{},
				kerrs.String("setting", "Indexes"), kerrs.String("problem", "a name longer than "+strconv.Itoa(MaxSQLIndexNameLen)+" bytes"),
				kerrs.String("index", spec.Name))
		}
	}
	//: declarations the tables can hold.
	return nil
}

// validateTransactor refuses a transactor the store could not join a
// transaction of, or hold its hooks until a commit of, and returns both
// capabilities otherwise.
func validateTransactor(tm coresql.Transactor) (txParts, error) {
	//: nothing to write through.
	if tm == nil {
		//: StoreMisconfigured, naming the setting.
		return txParts{}, misconfigured("Transactor", "nil")
	}
	joiner, joins := tm.(coresql.Joiner)
	deferrer, defers := tm.(coresql.Deferrer)
	//: without both, a call inside the caller's transaction would read outside
	//: it, or announce a write that may still be rolled back.
	if !joins || !defers {
		//: StoreMisconfigured, naming what is missing.
		return txParts{}, misconfigured("Transactor", "not a core/data/sql Joiner and Deferrer")
	}
	//: a transactor the store can work with.
	return txParts{join: joiner, hold: deferrer}, nil
}

// validateTable refuses a table name the store could not interpolate safely,
// or that another store's derived table could collide with.
func validateTable(table string) error {
	//: the shape, which is the injection defence.
	if !tablePattern.MatchString(table) {
		//: StoreMisconfigured; the name is the caller's own configuration.
		return kerrs.Wrap(coredocstore.StoreMisconfigured, kerrs.WrapParams{},
			kerrs.String("setting", "Table"), kerrs.String("problem", "not a lower-case SQL identifier"), kerrs.String("table", table))
	}
	//: room for the longest derived name within every engine's limit.
	if len(table) > MaxSQLTableLen {
		//: StoreMisconfigured, naming the limit.
		return kerrs.Wrap(coredocstore.StoreMisconfigured, kerrs.WrapParams{},
			kerrs.String("setting", "Table"), kerrs.String("problem", "longer than "+strconv.Itoa(MaxSQLTableLen)+" bytes"),
			kerrs.String("table", table))
	}
	//: the separator of derived names, which would let one store's index
	//: table be another store's documents.
	if strings.Contains(table, derivedSeparator) {
		//: StoreMisconfigured, naming the rule.
		return kerrs.Wrap(coredocstore.StoreMisconfigured, kerrs.WrapParams{},
			kerrs.String("setting", "Table"), kerrs.String("problem", "holds three underscores in a row"), kerrs.String("table", table))
	}
	//: SQLite's own names.
	if strings.HasPrefix(table, reservedTablePrefix) {
		//: StoreMisconfigured, naming the rule.
		return kerrs.Wrap(coredocstore.StoreMisconfigured, kerrs.WrapParams{},
			kerrs.String("setting", "Table"), kerrs.String("problem", "starts with sqlite_, which SQLite reserves"), kerrs.String("table", table))
	}
	//: a table name every engine takes.
	return nil
}

// indexTable derives the name of the table a store keeps its index rows in.
func indexTable(table string) string {
	//: the documents' table, the separator no table name holds, the tag.
	return table + derivedSeparator + indexTableTag
}

// versionsTable derives the name of the table a store keeps its versions in.
func versionsTable(table string) string {
	//: the documents' table, the separator no table name holds, the tag.
	return table + derivedSeparator + versionsTableTag
}
