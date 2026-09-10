// Package sql — hosts the only place in the SDK that renders dialect-specific
// SQL. Every statement this domain sends is built here.
package sql

import (
	"hash/fnv"
	"strconv"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
)

// savepointPrefix names every savepoint this package creates. It is a fixed
// SDK-owned prefix so a savepoint in a database log is attributable, and so a
// caller's own savepoints (if they issue any by hand) cannot collide with the
// runner's.
const savepointPrefix string = "ktn_sp_"

// decimalBase is the radix used when rendering counters into identifiers.
const decimalBase int = 10

// savepointName renders the identifier for nesting level n.
//
// The name is SDK-generated and never caller-supplied: an SQL identifier
// cannot be a bound parameter, so a caller-supplied name would be
// concatenated into a statement — an injection with the shape of a feature.
//
// n comes from a counter that never resets inside one transaction, so a name
// is never REUSED either. Reuse is legal in every supported dialect and it is
// a trap: `SAVEPOINT a` twice creates a second savepoint that shadows the
// first, and `RELEASE SAVEPOINT a` then releases only the inner one, leaving
// the outer alive under a name the caller believes is gone.
func savepointName(n uint64) string {
	//: prefix + counter, both SDK-controlled, so the result is always a bare
	//: identifier and never needs quoting.
	return savepointPrefix + strconv.FormatUint(n, decimalBase)
}

// savepointSQL renders the three savepoint statements for the dialect.
//
// All three supported dialects spell them IDENTICALLY — `SAVEPOINT n`,
// `ROLLBACK TO SAVEPOINT n`, `RELEASE SAVEPOINT n` — and that is not a
// coincidence, it is the selection criterion. The engines refused by name in
// core/sql spell nesting differently enough that it is a different algorithm:
// T-SQL has no RELEASE at all, Oracle has no RELEASE, Db2 requires a
// mandatory cursor-retention clause. The function still takes the dialect so
// that adding a fourth engine cannot be done by forgetting this file.
func savepointSQL(_ coresql.Dialect, name string) (create, rollback, release string) {
	//: one grammar for postgres, mysql and sqlite alike.
	return "SAVEPOINT " + name,
		"ROLLBACK TO SAVEPOINT " + name,
		"RELEASE SAVEPOINT " + name
}

// placeholder renders the n-th bind marker (1-based) for the dialect.
//
// This is the divergence that makes a "portable" hand-written query a fiction:
// PostgreSQL numbers its parameters, MySQL and SQLite do not. Every statement
// this package binds arguments to goes through here.
func placeholder(dialect coresql.Dialect, n int) string {
	//: PostgreSQL's ordinal form.
	if dialect == coresql.DialectPostgres {
		//: $1, $2, … — the position is part of the marker.
		return "$" + strconv.Itoa(n)
	}
	//: MySQL and SQLite both use the positional question mark.
	return "?"
}

// tryLockSQL renders the statement that attempts the advisory lock WITHOUT
// blocking, and the argument to bind to it.
//
// Both forms are non-blocking on purpose. A blocking acquisition would be
// bounded only by the caller's context, and a cancelled context racing a
// server-side grant can leave a lock held by a session nobody is watching.
// Trying, failing and retrying on an injected clock is race-free, uniform
// across the two engines, and testable without sleeping.
func tryLockSQL(dialect coresql.Dialect, key string) (query string, arg any) {
	//: PostgreSQL's advisory locks are keyed by a bigint, so the table name
	//: is folded into one.
	if dialect == coresql.DialectPostgres {
		//: returns true when the lock was taken, false when it was not.
		return "SELECT pg_try_advisory_lock(" + placeholder(dialect, 1) + ")", lockKey(key)
	}
	//: MySQL's GET_LOCK is keyed by a string and takes its own timeout; 0
	//: makes it the same non-blocking try.
	return "SELECT GET_LOCK(" + placeholder(dialect, 1) + ", 0)", key
}

// unlockSQL renders the statement that releases the advisory lock, and the
// argument to bind to it.
func unlockSQL(dialect coresql.Dialect, key string) (query string, arg any) {
	//: the mirror of tryLockSQL, on the SAME connection — both engines scope
	//: the lock to the session, so releasing it from a pooled connection
	//: would release nothing and report success.
	if dialect == coresql.DialectPostgres {
		//: unlocks the bigint key held by this session.
		return "SELECT pg_advisory_unlock(" + placeholder(dialect, 1) + ")", lockKey(key)
	}
	//: unlocks the named lock held by this session.
	return "SELECT RELEASE_LOCK(" + placeholder(dialect, 1) + ")", key
}

// lockKey folds a lock name into the 64-bit integer PostgreSQL's advisory
// locks are keyed by.
//
// FNV-1a rather than a cryptographic hash: the value is a namespace
// coordinate, not a secret, and a collision would only make two DIFFERENT
// migration sets serialise against each other — a performance surprise, never
// a correctness one. The result is reduced into the signed range because
// pg_advisory_lock takes a bigint.
func lockKey(name string) int64 {
	//: 64-bit FNV-1a over the raw name.
	hasher := fnv.New64a()
	//: hash.Hash documents that Write NEVER returns an error, so the only
	//: honest handling is to say so and move on: there is no failure mode to
	//: propagate and no caller who could act on one.
	if _, err := hasher.Write([]byte(name)); err != nil {
		//: unreachable by the hash.Hash contract; a zero key would serialise
		//: every migration set against every other, which fails SAFE.
		return 0
	}
	//: reinterpret as signed, which is what bigint is.
	return int64(hasher.Sum64())
}
