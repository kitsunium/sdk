// Package docstore — the siblings of Collection (ADR 0039): a document's
// versions, and the same two ports with a context on every call, which the SQL
// engine implements.
package docstore

import "context"

// Versioned is a document's versions, on a store whose calls take no context:
// the sibling of [Collection] the file engine implements beside it (ADR 0143).
// A store that keeps no versions answers every call with VERSIONS_NOT_KEPT,
// except the Stamped writes, which then store as their plain forms do.
type Versioned[T any] interface {
	// PutStamped is Put, with what the write says about the version it makes.
	PutStamped(v T, stamp StampValue) error
	// InsertStamped is Insert, with what the write says about the version it
	// makes: version 1, whatever stamp.InPlace says.
	InsertStamped(v T, stamp StampValue) error
	// ReplaceStamped is Replace, with what the write says about the version it
	// makes.
	ReplaceStamped(v T, stamp StampValue) error
	// UpdateStamped is Update, with what the write says about the version it
	// makes.
	UpdateStamped(key string, stamp StampValue, fn func(*T) error) (T, error)
	// Versions returns the versions kept of the document stored under key,
	// newest first: the current one — the document as it is now — then the
	// former ones. DOCUMENT_NOT_FOUND when no document is stored under key.
	Versions(key string) ([]VersionValue, error)
	// Version returns the version numbered number of the document stored
	// under key: VERSION_NOT_FOUND when the document keeps none of that
	// number — never made, or pruned.
	Version(key string, number uint64) (VersionValue, error)
	// RewriteVersions rewrites the former versions of the document stored
	// under key in one durable write: fn receives copies of them, newest
	// first, and returns those to keep, in the same order. It may drop a
	// version and change what one holds and what its writer said, and nothing
	// else — VERSIONS_REWRITE_REFUSED otherwise. An error from fn is returned
	// as it is and changes nothing. It calls no hook: the document did not
	// change.
	RewriteVersions(key string, fn func(former []VersionValue) ([]VersionValue, error)) error
}

// CollectionContext is [Collection] with a context on every call — the port
// the SQL engine, internal/service/data/docstore.SQLStore, implements. A call
// runs on the transaction its context carries for the store's database, and on
// the pool otherwise; a write is atomic on its own either way, and its hooks
// run once the transaction that holds it has committed (ADR 0139). Every
// method answers as its Collection counterpart does, and may also fail as a
// database fails.
//
// The SQL engine's Count and Reindex are the engine's own: a double of the
// port has no tables to count or to file again.
type CollectionContext[T any] interface {
	Announcer
	// Get is Collection.Get under ctx.
	Get(ctx context.Context, key string) (T, error)
	// List is Collection.List under ctx.
	List(ctx context.Context) ([]T, error)
	// Filter is Collection.Filter under ctx.
	Filter(ctx context.Context, keep func(T) bool) ([]T, error)
	// Entries is Collection.Entries under ctx.
	Entries(ctx context.Context, limit int) ([]EntryValue, error)
	// Lookup is Collection.Lookup under ctx.
	Lookup(ctx context.Context, index, key string) (T, error)
	// Find is Collection.Find under ctx.
	Find(ctx context.Context, index, key string) ([]T, error)
	// Put is Collection.Put under ctx.
	Put(ctx context.Context, v T) error
	// Insert is Collection.Insert under ctx.
	Insert(ctx context.Context, v T) error
	// Replace is Collection.Replace under ctx.
	Replace(ctx context.Context, v T) error
	// Update is Collection.Update under ctx: fn runs inside the write's
	// transaction, and must not write the document it updates.
	Update(ctx context.Context, key string, fn func(*T) error) (T, error)
	// Delete is Collection.Delete under ctx.
	Delete(ctx context.Context, key string) error
}

// VersionedContext is [Versioned] with a context on every call: the sibling of
// [CollectionContext] the SQL engine implements beside it. A write's version
// rows travel in that write's own transaction.
type VersionedContext[T any] interface {
	// PutStamped is Versioned.PutStamped under ctx.
	PutStamped(ctx context.Context, v T, stamp StampValue) error
	// InsertStamped is Versioned.InsertStamped under ctx.
	InsertStamped(ctx context.Context, v T, stamp StampValue) error
	// ReplaceStamped is Versioned.ReplaceStamped under ctx.
	ReplaceStamped(ctx context.Context, v T, stamp StampValue) error
	// UpdateStamped is Versioned.UpdateStamped under ctx.
	UpdateStamped(ctx context.Context, key string, stamp StampValue, fn func(*T) error) (T, error)
	// Versions is Versioned.Versions under ctx.
	Versions(ctx context.Context, key string) ([]VersionValue, error)
	// Version is Versioned.Version under ctx.
	Version(ctx context.Context, key string, number uint64) (VersionValue, error)
	// RewriteVersions is Versioned.RewriteVersions under ctx.
	RewriteVersions(ctx context.Context, key string, fn func(former []VersionValue) ([]VersionValue, error)) error
}
