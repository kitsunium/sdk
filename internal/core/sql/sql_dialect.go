// Package sql — hosts Dialect, the closed set of SQL engines this domain can
// spell, and the two refusals that keep it closed.
package sql

import "github.com/kitsunium/sdk/internal/kernel/errs"

// Dialect names a SQL engine whose savepoint grammar, placeholder syntax and
// advisory-lock mechanism this SDK knows exactly.
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
// serialises writers. That distinction decides whether [Migrator] can promise
// mutual exclusion across processes, so it is a property of the dialect and
// not a runtime discovery (ADR 0055 §D7).
func (d Dialect) SupportsAdvisoryLock() bool {
	//: Postgres has pg_advisory_lock, MySQL has GET_LOCK; both die with the
	//: session, which is the entire reason they were chosen.
	return d == DialectPostgres || d == DialectMySQL
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
