package plug

import (
	"context"
	"encoding/json"
	"iter"
)

// The store subsystem's engine and its siblings (ADR 0179 §2, §3), which
// the core's engines implement since step 2 — in place of its former
// storeEngine[T], folder, keyser, versionKeeper, versionWeighter and
// trasher —, and kit's scope on a database.

// Database is kit's scope on one database (ADR 0179 §2): the transactions
// kit.Transact opens on it, their savepoints, and the functions held until
// their end. The core knows a database only through it; the store package
// implements it over the SDK's sql.
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

// StoreEngine is what a store runs on, over documents (ADR 0179 §2): the
// non-generic engine under Store[T]. Its refusals are kit's: the engine
// translates the SDK's before they reach the core. Every call takes the
// caller's context, which carries the transaction the call joins. It grows by
// the sibling interfaces below, never by a method (the SDK's ADR 0039).
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
