package plug

import (
	"context"
	"database/sql"
	"encoding/json"
	"iter"
	"time"
)

// The store subsystem's engine and its siblings (ADR 0179 §2, §3), which
// framework/internal/app/storeengine implements over the SDK's document store,
// and the core's engines that wrap one hand on; kit's scope on a database and
// its migrations; and kit's transaction, which the core implements for the
// engines to take part in.

// Database is kit's scope on one database (ADR 0179 §2): the transactions
// app.Transact opens on it, their savepoints, and the functions held until
// their end. The core knows a database only through it; the store package
// implements it over the SDK's sql. It grows by the siblings below.
type Database interface {
	// Transact runs fn in a transaction of the database, or in a savepoint
	// of the one ctx carries for it: nil commits or releases, an error
	// rolls back.
	Transact(ctx context.Context, fn func(ctx context.Context) error) error
	// InTransaction reports whether ctx carries a transaction of this
	// database.
	InTransaction(ctx context.Context) bool
	// Defer holds fn until the transaction ctx carries for the database
	// commits, and reports true; outside one it holds nothing and reports
	// false.
	Defer(ctx context.Context, fn func()) bool
	// OnAbort holds fn until the work of the scope ctx carries for the
	// database is undone, and reports true; outside one it holds nothing
	// and reports false.
	OnAbort(ctx context.Context, fn func(ctx context.Context)) bool
}

// Checker is a Database that says whether it answers. A sibling of
// [Database].
type Checker interface {
	// Check asks the database whether it answers, within its check timeout.
	Check(ctx context.Context) error
}

// MigratorProvider is a Database that runs a set of migrations under a version
// table of its own, and so a lock of its own. A sibling of [Database].
type MigratorProvider interface {
	// Migrator is the set ms, under the version table table.
	Migrator(table string, ms []MigrationValue) (Migrator, error)
}

// Migrator is one set of migrations on a database.
type Migrator interface {
	// Plan is the set's pending migrations, oldest first, applying nothing.
	Plan(ctx context.Context) ([]MigrationValue, error)
	// Up applies every pending migration, in order.
	Up(ctx context.Context) error
	// Down reverses the applied migrations above version, newest first.
	Down(ctx context.Context, version uint64) error
}

// Executor is what a migration's step runs its statements on: the
// transaction its migration runs in.
type Executor interface {
	// ExecContext runs a statement that returns no rows.
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	// QueryContext runs a query that returns rows.
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	// QueryRowContext runs a query that returns at most one row.
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Transaction is kit's transaction as a store's engine takes part in it
// (ADR 0139, ADR 0178): the core implements it, an engine calls it with its
// caller's context.
type Transaction interface {
	// Local reports whether ctx runs in a transaction that holds the data's
	// writer turn: a write in the data directory or in memory then records
	// what it replaces.
	Local(ctx context.Context) bool
	// Undo keeps restore, which a rollback of the transaction ctx carries
	// runs; nothing outside one. store names the store in a log line.
	Undo(ctx context.Context, store string, restore func(ctx context.Context) error)
	// AtCommit holds fn until the transaction ctx carries commits, and
	// reports whether it did: false outside one, where the caller runs it
	// now.
	AtCommit(ctx context.Context, fn func()) bool
	// Retell keeps fn, which announces a write on a database again when the
	// transaction ctx carries rolls it back.
	Retell(ctx context.Context, fn func())
}

// StoreEngine is what a store runs on, over documents (ADR 0179 §2): the
// non-generic engine under Store[T]. Its refusals are kit's ([Refusal]): the
// engine translates the SDK's before they reach the core. Every call takes
// the caller's context, which carries the transaction the call joins. It
// grows by the sibling interfaces below, never by a method (the SDK's ADR
// 0039).
type StoreEngine interface {
	// Get returns the document under key.
	Get(ctx context.Context, key string) (DocumentValue, error)
	// List returns every document, in key order.
	List(ctx context.Context) ([]DocumentValue, error)
	// Filter returns the documents keep accepts, in key order.
	Filter(ctx context.Context, keep func(d DocumentValue) bool) ([]DocumentValue, error)
	// Lookup returns the document a unique index files under key.
	Lookup(ctx context.Context, index, key string) (DocumentValue, error)
	// Find returns the documents an index files under key, in key order.
	Find(ctx context.Context, index, key string) ([]DocumentValue, error)
	// Count says how many documents the store holds.
	Count(ctx context.Context) (int, error)
	// Entries returns up to limit documents as they rest, in key order.
	Entries(ctx context.Context, limit int) ([]json.RawMessage, error)
	// Write stores d as mode says. The document and its index keys are kept
	// together, or neither.
	Write(ctx context.Context, d DocumentValue, mode WriteMode) error
	// Update applies fn to the document under key and stores the result,
	// atomically with respect to every other write of the store.
	Update(ctx context.Context, key string, fn func(d *DocumentValue) error) (DocumentValue, error)
	// Delete removes the document under key.
	Delete(ctx context.Context, key string) error
	// Watch registers the functions told the key of every document written
	// and deleted, once the write stands.
	Watch(onWrite, onDelete func(key string))
	// Close releases the engine.
	Close() error
}

// Folder is an engine that keeps what its writes left beside its data, and
// can fold it in now. A sibling of [StoreEngine].
type Folder interface {
	// Fold rewrites the store's data with only what it holds now.
	Fold(ctx context.Context) error
}

// Keyser is an engine that lists the keys of its documents, in key order,
// above after, without reading a document. A sibling of [StoreEngine].
type Keyser interface {
	// Keys yields the keys above after, in order, then its failure if any.
	Keys(ctx context.Context, after string) iter.Seq2[string, error]
}

// EntrySource is an engine that lists its records as they rest with their keys:
// what a sealing store reads its records by. A sibling of [StoreEngine].
type EntrySource interface {
	// KeyedEntries returns up to limit records as they rest, in key order.
	KeyedEntries(ctx context.Context, limit int) ([]EntryValue, error)
}

// Reindexer is an engine on a database that files its index rows again. A
// sibling of [StoreEngine].
type Reindexer interface {
	// Reindex files every record's index rows again.
	Reindex(ctx context.Context) error
}

// VersionKeeper is an engine that keeps its records' versions. A sibling of
// [StoreEngine].
type VersionKeeper interface {
	// StoredVersions are the versions of the record under key, newest
	// first, the current one first.
	StoredVersions(ctx context.Context, key string) ([]VersionValue, error)
	// RewriteVersions rewrites the former versions of the record under key
	// as fn returns them: fn may drop a version or change what one holds,
	// never add or renumber one.
	RewriteVersions(ctx context.Context, key string, fn func(vs []VersionValue) ([]VersionValue, error)) error
}

// VersionWeighter is an engine that says what its records' versions weigh.
// A sibling of [StoreEngine].
type VersionWeighter interface {
	// VersionWeight is what the store's versions and trash weigh.
	VersionWeight(ctx context.Context) (VersionWeightValue, error)
}

// Trasher is an engine that keeps a trash. A sibling of [StoreEngine].
type Trasher interface {
	// Trash moves the record under key to the trash: gone from every read,
	// its index keys freed, its versions kept.
	Trash(ctx context.Context, key string) error
	// Untrash brings the record trashed under key back as it was.
	Untrash(ctx context.Context, key string) error
	// Purge empties the trash of the record trashed under key, its versions
	// with it.
	Purge(ctx context.Context, key string) error
	// Tombstones are every record in the trash, in key order.
	Tombstones(ctx context.Context) ([]TombstoneValue, error)
}

// DocumentSource is an engine that says the document store it runs on: itself,
// or the one what it wraps runs on. A data copy reads and writes a store
// through it (ADR 0178 C7). A sibling of [StoreEngine].
type DocumentSource interface {
	// Documents is the document store the engine runs on, and whether there
	// is one.
	Documents() (Documents, bool)
}

// Documents is a store's documents as they rest, as a data copy reads them.
type Documents interface {
	// KeyList yields every key the store holds, in key order.
	KeyList(ctx context.Context) iter.Seq2[string, error]
	// TombstoneList is the trash, in key order: none when the store keeps
	// no versions.
	TombstoneList(ctx context.Context) ([]TombstoneValue, error)
	// VersionList is the versions of the document key, newest first.
	VersionList(ctx context.Context, key string) ([]VersionValue, error)
	// Current is the document key as the store keeps it.
	Current(ctx context.Context, key string) (json.RawMessage, error)
	// KeepsVersions reports whether the store keeps versions.
	KeepsVersions() bool
}

// DocumentSink is a store's documents on the database a data copy writes. A
// sibling of [Documents].
type DocumentSink interface {
	// PutDocument writes the document key with its versions and its trash
	// as they are given.
	PutDocument(ctx context.Context, key string, versions []VersionValue, trashedAt time.Time) error
	// InsertRaw writes a document of a store that keeps no versions, as it
	// rests.
	InsertRaw(ctx context.Context, raw json.RawMessage) error
}
