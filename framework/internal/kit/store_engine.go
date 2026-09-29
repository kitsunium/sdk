// Package kit — the engines a store runs on: memory, files or a database.
package kit

import (
	"context"
	"encoding/json"

	"github.com/kitsunium/sdk/pkg/v1/docstore"
	"github.com/kitsunium/sdk/pkg/v1/vfs"
)

// A store is a port: Service.Store[T] is what a service declares and calls,
// its API and its refusals are kit's, and the backend it runs on is the
// app's choice (ADR 0004). storeEngine is that backend, as kit sees it.

// storeEngine is what a store runs on. Store[T] holds one while its app
// runs, opened by the app's placement of the store: in memory or in the data
// directory today — both the SDK's document store (docEngine) — and, once
// the SDK ships its SQL document store, on a database.
//
// An engine's refusals are the SDK document store's sentinels —
// docstore.DocumentNotFound, DocumentExists, UniqueKeyTaken, DocumentKeyEmpty,
// DocumentKeyChanged, IndexUnknown, IndexNotUnique, DocumentUndecodable,
// DocumentUnencodable, PersistFailed, StoreClosed, WriteUnconfirmed —, which
// Store.said words for the caller: every engine speaks them, so said speaks
// for every engine unchanged, and no engine quotes a key in an error.
//
// Every call takes the caller's context: an engine on a database runs the
// call on the transaction the context carries for it and within its
// timeout. The document store needs none. The port grows by sibling
// interfaces an engine may implement, never by a method (the SDK's ADR 0039).
type storeEngine[T any] interface {
	// Get returns the entity under key.
	Get(ctx context.Context, key string) (T, error)
	// List returns every entity, in key order.
	List(ctx context.Context) ([]T, error)
	// Filter returns the entities keep accepts, in key order.
	Filter(ctx context.Context, keep func(T) bool) ([]T, error)
	// Lookup returns the entity a unique index files under key.
	Lookup(ctx context.Context, index, key string) (T, error)
	// Find returns the entities an index files under key, in key order.
	Find(ctx context.Context, index, key string) ([]T, error)
	// Count says how many entities the store holds.
	Count(ctx context.Context) (int, error)
	// Entries returns up to limit entities as the store keeps them, in key
	// order: the JSON kit encoded, for the Studio.
	Entries(ctx context.Context, limit int) ([]json.RawMessage, error)
	// Write stores v as mode says: anything under its key, nothing, or an
	// entity. The entity and its index keys are kept together, or neither.
	Write(ctx context.Context, v T, mode writeMode) error
	// Update applies fn to the entity under key and stores the result,
	// atomically with respect to every other write of the store.
	Update(ctx context.Context, key string, fn func(*T) error) (T, error)
	// Delete removes the entity under key.
	Delete(ctx context.Context, key string) error
	// Watch registers the functions told the key of every entity written
	// and deleted, once the write stands, outside every lock of the engine.
	// kit registers once, when the store starts.
	Watch(onWrite, onDelete func(key string))
	// Close releases the engine; a document store folds what was written
	// into its snapshot first.
	Close() error
}

// folder is an engine that keeps what its writes left beside its data —
// the document store's small files beside its snapshot — and can fold them
// into it now, so that what an erasure overwrote leaves the files (ADR
// 0006). A sibling of the port: an engine that keeps nothing beside its data
// does not implement it.
type folder interface {
	Fold(ctx context.Context) error
}

// storeFS is where a document store keeps its files: the data directory and
// the store's snapshot in it. A nil FS keeps the store in memory.
type storeFS struct {
	fs   vfs.FullFS
	path string
}

// docEngine is a store on the SDK's document store: in memory, or one
// snapshot and the small files written since in the data directory. The
// document store keeps every entity in memory, persists each write before
// returning, and keeps the indexes in step with the entities.
type docEngine[T any] struct{ ds *docstore.Store[T] }

// openDocEngine opens the document store of a store: loaded from where fs
// says — the snapshot, what was written since replayed on it, the indexes
// rebuilt — or empty in memory.
func openDocEngine[T any](key func(T) string, where storeFS, specs []docstore.IndexSpec[T]) (*docEngine[T], error) {
	ds, err := docstore.Open(docstore.Config[T]{Key: key, FS: where.fs, Path: where.path}, specs...)
	if err != nil {
		return nil, err
	}
	return &docEngine[T]{ds: ds}, nil
}

// Get reads the document key.
//
//ktn:allow-unused-param: the storeEngine interface passes a context the local document store does not take
func (e *docEngine[T]) Get(_ context.Context, key string) (T, error) { return e.ds.Get(key) }

// List reads every document.
//
//ktn:allow-unused-param: the storeEngine interface passes a context the local document store does not take
func (e *docEngine[T]) List(_ context.Context) ([]T, error) { return e.ds.List() }

// Filter reads the documents keep accepts.
//
//ktn:allow-unused-param: the storeEngine interface passes a context the local document store does not take
func (e *docEngine[T]) Filter(_ context.Context, keep func(T) bool) ([]T, error) {
	return e.ds.Filter(keep)
}

// Lookup reads the document a unique index names by key.
//
//ktn:allow-unused-param: the storeEngine interface passes a context the local document store does not take
func (e *docEngine[T]) Lookup(_ context.Context, index, key string) (T, error) {
	return e.ds.Lookup(index, key)
}

// Find reads every document an index names by key.
//
//ktn:allow-unused-param: the storeEngine interface passes a context the local document store does not take
func (e *docEngine[T]) Find(_ context.Context, index, key string) ([]T, error) {
	return e.ds.Find(index, key)
}

// Count is how many documents the store holds.
//
//ktn:allow-unused-param: the storeEngine interface passes a context the local document store does not take
func (e *docEngine[T]) Count(_ context.Context) (int, error) { return e.ds.Stats().Documents, nil }

// Entries are the latest limit documents, raw.
//
//ktn:allow-unused-param: the storeEngine interface passes a context the local document store does not take
func (e *docEngine[T]) Entries(_ context.Context, limit int) ([]json.RawMessage, error) {
	entries, err := e.ds.Entries(limit)
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, len(entries))
	for i, en := range entries {
		out[i] = en.JSON
	}
	return out, nil
}

// Write writes v in mode.
//
//ktn:allow-unused-param: the storeEngine interface passes a context the local document store does not take
func (e *docEngine[T]) Write(_ context.Context, v T, mode writeMode) error {
	switch mode {
	case insertOnly:
		return e.ds.Insert(v)
	case replaceOnly:
		return e.ds.Replace(v)
	case upsert:
		return e.ds.Put(v)
	default:
		return e.ds.Put(v)
	}
}

// Update changes the document key with fn, under the store's lock.
//
//ktn:allow-unused-param: the storeEngine interface passes a context the local document store does not take
func (e *docEngine[T]) Update(_ context.Context, key string, fn func(*T) error) (T, error) {
	return e.ds.Update(key, fn)
}

// Delete removes the document key.
//
//ktn:allow-unused-param: the storeEngine interface passes a context the local document store does not take
func (e *docEngine[T]) Delete(_ context.Context, key string) error { return e.ds.Delete(key) }

// Watch calls onWrite and onDelete after each write and removal.
func (e *docEngine[T]) Watch(onWrite, onDelete func(key string)) {
	e.ds.OnWrite(onWrite)
	e.ds.OnDelete(onDelete)
}

// Close closes the document store.
func (e *docEngine[T]) Close() error { return e.ds.Close() }

// Fold rewrites the store's log with only what it holds now.
func (e *docEngine[T]) Fold(_ context.Context) error { return e.ds.Fold() }
