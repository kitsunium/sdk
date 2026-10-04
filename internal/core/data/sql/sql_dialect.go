// Package sql — hosts Dialect, the closed set of SQL engines this domain can
// spell, the vocabulary each one spells statements with, and the two refusals
// that keep the set closed.
//
// Package sql — hosts the four ports themselves. Kept apart from sql.go so
// the package's contract is one file: what a caller may implement, and what
// the SDK promises to accept.
package sql

import (
	"strconv"
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Dialect names a SQL engine whose savepoint grammar, placeholder syntax,
// identifier quoting, row-lock clauses and advisory-lock mechanism this SDK
// knows exactly.
//
// The set is CLOSED and small on purpose. Every feature this domain adds — a
// savepoint, a `CREATE TABLE IF NOT EXISTS`, an advisory lock — is spelled
// differently by each engine, and there is no portable subset that covers all
// three. So the domain refuses a dialect it cannot spell AT CONSTRUCTION,
// by name, rather than generating SQL that will fail at the first savepoint of
// the first nested transaction on a production database (ADR 0055 §D4).
//
// The zero value is [DialectUnknown] and is not usable: it is what an unset
// configuration field looks like, and reading it as "probably Postgres" is how
// a MySQL deployment discovers the difference in production (ADR 0031).
type Dialect uint8

const (
	// DialectUnknown is the unusable zero value.
	DialectUnknown Dialect = iota
	// DialectPostgres is PostgreSQL 9.1+ (and wire-compatible engines that
	// implement pg_advisory_lock, e.g. CockroachDB advertises the function
	// but the caller must verify it is a real lock before claiming this).
	DialectPostgres
	// DialectMySQL is MySQL 5.7+ / MariaDB 10.x with InnoDB.
	DialectMySQL
	// DialectSQLite is SQLite 3.6.8+.
	DialectSQLite
)

// The three lookup tables ParseDialect and String are built from. Grouped so a
// new dialect that forgets one of them is visible in one place rather than
// rendering as "unknown" at a call site.
var (
	// dialectNames maps a Dialect to its canonical lowercase name. Kept beside
	// the constants so a new dialect that forgets its name is visible here.
	dialectNames = map[Dialect]string{
		DialectUnknown:  "unknown",
		DialectPostgres: "postgres",
		DialectMySQL:    "mysql",
		DialectSQLite:   "sqlite",
	}

	// dialectByName is the accepted half of [ParseDialect]. Aliases are
	// included where the ecosystem genuinely uses two spellings for one
	// engine, so a caller reading their driver's own documentation does not
	// get refused for a synonym.
	dialectByName = map[string]Dialect{
		"postgres":   DialectPostgres,
		"postgresql": DialectPostgres,
		"pgx":        DialectPostgres,
		"mysql":      DialectMySQL,
		"mariadb":    DialectMySQL,
		"sqlite":     DialectSQLite,
		"sqlite3":    DialectSQLite,
	}

	// refusedDialects is the REFUSED-BY-NAME half of [ParseDialect]: engines
	// this SDK recognises and declines, each with the reason it cannot be
	// supported by the same code path as the three above.
	//
	// Being on this list is a stronger statement than being absent from
	// dialectByName. "I have never heard of this" and "I know this engine and
	// its savepoint grammar is not the one I implement" are different facts,
	// and a caller debugging a refusal deserves to know which one they hit.
	refusedDialects = map[string]string{
		//: T-SQL spells a savepoint `SAVE TRANSACTION n`, has NO release
		//: statement at all, and dooms a transaction on many errors so that
		//: even `ROLLBACK TRANSACTION n` fails (XACT_STATE() = -1). Nesting
		//: there is a different algorithm, not a different string.
		"sqlserver": "T-SQL uses SAVE TRANSACTION and has no RELEASE; nesting is a different algorithm",
		"mssql":     "T-SQL uses SAVE TRANSACTION and has no RELEASE; nesting is a different algorithm",
		//: Oracle has SAVEPOINT and ROLLBACK TO, but no RELEASE SAVEPOINT: a
		//: savepoint lives until the transaction ends. This runner releases
		//: on success so a long transaction does not accumulate them, which
		//: Oracle cannot express.
		"oracle": "Oracle has no RELEASE SAVEPOINT, so a released nested scope cannot be expressed",
		"godror": "Oracle has no RELEASE SAVEPOINT, so a released nested scope cannot be expressed",
		//: Db2 spells it `SAVEPOINT n ON ROLLBACK RETAIN CURSORS` and the
		//: cursor retention clause is mandatory and semantically load-bearing.
		"db2": "Db2 requires the ON ROLLBACK RETAIN clause, which changes cursor semantics",
	}
)

// String returns the dialect's canonical lowercase name.
func (d Dialect) String() string {
	//: an unmapped value renders as the zero value's name rather than a
	//: number, so a log line never shows "Dialect(7)".
	if name, ok := dialectNames[d]; ok {
		//: the canonical spelling.
		return name
	}
	//: any value outside the closed set is, by definition, unknown.
	return dialectNames[DialectUnknown]
}

// Valid reports whether d is a dialect this domain can actually spell.
func (d Dialect) Valid() bool {
	//: DialectUnknown is the only in-range value that is not usable.
	return d == DialectPostgres || d == DialectMySQL || d == DialectSQLite
}

// SupportsAdvisoryLock reports whether the engine offers a SESSION-SCOPED
// advisory lock — one the server releases when the connection holding it
// drops.
//
// SQLite does not: it has no advisory-lock function, only the file lock that
// serialises writers. That distinction decides HOW [Migrator] promises mutual
// exclusion across processes, so it is a property of the dialect and not a
// runtime discovery (ADR 0055 §D7). On SQLite the runner holds the database
// file's write lock for the whole run instead, which the operating system
// releases when its holder dies, as a server releases an advisory lock
// (ADR 0140).
func (d Dialect) SupportsAdvisoryLock() bool {
	//: Postgres has pg_advisory_lock, MySQL has GET_LOCK; both die with the
	//: session, which is the entire reason they were chosen.
	return d == DialectPostgres || d == DialectMySQL
}

// Placeholder renders the bind marker of a statement's n-th argument, counted
// from 1.
//
// This is the divergence that makes a "portable" hand-written query a fiction:
// PostgreSQL numbers its markers — $1, $2 — while MySQL and SQLite share the
// positional ?, whose position is the argument's order. Every statement the SDK
// binds an argument to takes its markers from here, so a fourth engine is
// spelled in one place or not at all.
//
// A dialect that is not [Dialect.Valid] — an unset field — has no marker and
// renders the empty string, so a statement built from it parses on no engine
// rather than on whichever one a guess favoured (ADR 0031).
func (d Dialect) Placeholder(n int) string {
	//: PostgreSQL's ordinal form.
	if d == DialectPostgres {
		//: $1, $2, … — the position is part of the marker.
		return "$" + strconv.Itoa(n)
	}
	//: MySQL and SQLite both use the positional question mark.
	if d == DialectMySQL || d == DialectSQLite {
		//: ?, ?, … — the position is the argument's order.
		return "?"
	}
	//: no engine's marker for a dialect nobody chose.
	return ""
}

// QuoteIdent renders name as an identifier of the dialect: delimited, so a
// table called "order" or "user" is a name rather than a keyword, and with the
// delimiter doubled wherever the name holds it, which is how every engine
// spells a delimiter inside a delimited identifier. MySQL delimits with
// backticks, whatever its ANSI_QUOTES mode says; PostgreSQL and SQLite with
// the standard's double quotes.
//
// It validates nothing. An identifier cannot be a bound argument, so the SDK
// interpolates only names it validated at construction, and that check is the
// whole defence: what a name may hold, and how long it may be — PostgreSQL
// TRUNCATES a longer one, into what may be another table's name — is the
// caller's to refuse before quoting it. A dialect that is not [Dialect.Valid]
// renders the empty string, for the reason [Dialect.Placeholder] gives.
func (d Dialect) QuoteIdent(name string) string {
	//: MySQL's backticks.
	if d == DialectMySQL {
		//: `name`, a backtick inside it doubled.
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	}
	//: the standard's double quotes, which PostgreSQL and SQLite share.
	if d == DialectPostgres || d == DialectSQLite {
		//: "name", a double quote inside it doubled.
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
	//: no engine's quoting for a dialect nobody chose.
	return ""
}

// ForUpdate renders the clause that ends a read by locking the rows it returns
// until the transaction ends, leading space included: " FOR UPDATE" on
// PostgreSQL and MySQL. On MySQL it is also a locking read, which reads the
// latest committed rows rather than a REPEATABLE READ transaction's snapshot.
//
// SQLite has no row lock and no such clause, and renders the empty string: a
// SQLite transaction excludes every other writer with the database's one write
// lock, which it takes at its first WRITE, so a transaction that must read
// under exclusion writes first (ADR 0140). A dialect that is not
// [Dialect.Valid] renders the clause: an engine without one refuses the
// statement, which is louder than a lock dropped in silence.
func (d Dialect) ForUpdate() string {
	//: SQLite: the database's write lock is the exclusion.
	if d == DialectSQLite {
		//: nothing to add.
		return ""
	}
	//: the row lock both PostgreSQL and MySQL take on a locking read.
	return " FOR UPDATE"
}

// ForUpdateSkipLocked renders the clause that ends a read by locking the rows
// it returns and SKIPPING those another transaction holds, leading space
// included: " FOR UPDATE SKIP LOCKED" on PostgreSQL and MySQL. Two readers
// then never wait on each other's rows and never take the same one.
//
// It needs PostgreSQL 9.5, MySQL 8.0.1 or MariaDB 10.6 — later than the
// floors [DialectPostgres] and [DialectMySQL] name — and an older server
// refuses the statement. SQLite renders the empty string and a dialect that is
// not [Dialect.Valid] the clause, for the reasons [Dialect.ForUpdate] gives: a
// SQLite transaction that has written holds the database's only write lock,
// so no row it reads is held by anybody else.
func (d Dialect) ForUpdateSkipLocked() string {
	//: SQLite: the database's write lock is the exclusion.
	if d == DialectSQLite {
		//: nothing to add.
		return ""
	}
	//: locked, and never waited for.
	return " FOR UPDATE SKIP LOCKED"
}

// ParseDialect resolves a dialect name. It never guesses.
//
// A name in the accepted set resolves. A name in the refused set returns
// [DialectRefused] carrying WHY, so the caller learns their engine is known
// and declined rather than unrecognised. Anything else returns
// [UnknownDialect].
func ParseDialect(name string) (dialect Dialect, err error) {
	//: the accepted set first — the common path costs one map lookup.
	if dialect, ok := dialectByName[name]; ok {
		//: a dialect this domain can spell end to end.
		return dialect, nil
	}
	//: a name we recognise and decline is a DIFFERENT answer from a name we
	//: have never seen, and the caller is told which.
	if reason, ok := refusedDialects[name]; ok {
		//: the reason travels in Private and in a field, never in Public:
		//: Public is wire-safe and must not describe the caller's stack.
		return DialectUnknown, errs.Wrap(DialectRefused, errs.WrapParams{},
			errs.String("dialect", name), errs.String("reason", reason))
	}
	//: never heard of it — refuse rather than fall back to a default.
	return DialectUnknown, errs.Wrap(UnknownDialect, errs.WrapParams{},
		errs.String("dialect", name))
}
