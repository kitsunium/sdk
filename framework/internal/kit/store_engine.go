// Package kit — the engines a store runs on: memory, files or a database.
package kit

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/kitsunium/sdk/pkg/v1/docstore"
	"github.com/kitsunium/sdk/pkg/v1/vfs"
)

// A store is a port: Service.Store[T] is what a service declares and calls,
// its API and its refusals are kit's, and the backend it runs on is the
// app's choice (ADR 0004). storeEngine is that backend, as kit sees it.

// storeEngine is what a store runs on. Store[T] holds one while its app
// runs, opened by the app's placement of the store: in memory or in the data
// directory — the SDK's document store (docEngine) —, or on a database — the
// SDK's document store over SQL (sqlEngine, store_sql.go).
//
// An engine's refusals are the SDK document store's sentinels —
// docstore.DocumentNotFound, DocumentExists, UniqueKeyTaken, DocumentKeyEmpty,
// DocumentKeyChanged, IndexUnknown, IndexNotUnique, DocumentUndecodable,
// DocumentUnencodable, PersistFailed, StoreClosed, WriteUnconfirmed, and on a
// database StatementFailed and KeyTooLong —, which Store.said words for the
// caller: every engine speaks them, so said speaks for every engine
// unchanged, and no engine quotes a key in an error.
//
// Every call takes the caller's context: an engine on a database runs the
// call on the transaction the context carries for it and within its
// timeout; the document store records, in a transaction of the data's, what
// a write replaced, and holds the write's hooks until the commit. The port
// grows by sibling interfaces an engine may implement, never by a method
// (the SDK's ADR 0039).
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
	// and deleted, once the write stands — at the commit of the transaction
	// that made it —, outside every lock of the engine. kit registers once,
	// when the store starts.
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
//
// The data directory and memory have no transactions: in one of kit's
// (transact.go), a write records the value it replaces, which a rollback
// writes back, and its hooks wait for the commit.
type docEngine[T any] struct {
	ds  *docstore.Store[T]
	key func(T) string
	// name is the store's, which a rollback that cannot write a value back
	// names in the log.
	name string
	// keep is how many former versions the document store keeps of each
	// record (ADR 0007 §3): none when zero.
	keep int
	// undoing are the keys a rollback writes back, whose versions no write
	// prunes meanwhile (before).
	undoing sync.Map
	// onWrite and onDelete are what Watch registered.
	onWrite, onDelete func(key string)
}

// openDocEngine opens the document store of a store: loaded from where fs
// says — the snapshot, what was written since replayed on it, the indexes
// rebuilt — or empty in memory; keeping each record's versions as vs says.
func openDocEngine[T any](key func(T) string, where storeFS, specs []docstore.IndexSpec[T], vs versionsOn) (*docEngine[T], error) {
	e := &docEngine[T]{key: key, name: where.path, keep: vs.keep}
	cfg := docstore.Config[T]{Key: key, FS: where.fs, Path: where.path}
	if vs.keep > 0 {
		cfg.Versions, cfg.Clock = vs.keep, vs.clock
		// Asked under the writers' lock by a write that would prune: it may
		// read this store, and kit's holds. A rollback's write back prunes
		// nothing: what it takes out of the history is its own (forget).
		cfg.Held = func(key string) bool {
			if _, undoing := e.undoing.Load(key); undoing {
				return true
			}
			return vs.held != nil && vs.held(context.Background(), key)
		}
	}
	ds, err := docstore.Open(cfg, specs...)
	if err != nil {
		return nil, err
	}
	e.ds = ds
	return e, nil
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

// KeyedEntries are up to limit entities as the store keeps them, with
// their keys: what a sealing store reads its records by (seal_store.go).
func (e *docEngine[T]) KeyedEntries(_ context.Context, limit int) ([]docstore.Entry, error) {
	return e.ds.Entries(limit)
}

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
func (e *docEngine[T]) Write(ctx context.Context, v T, mode writeMode) error {
	key := e.keyOf(v)
	undo := e.before(ctx, key)
	stamp := e.stamp(ctx)
	var err error
	switch mode {
	case insertOnly:
		err = e.ds.InsertStamped(v, stamp)
	case replaceOnly:
		err = e.ds.ReplaceStamped(v, stamp)
	case upsert:
		err = e.ds.PutStamped(v, stamp)
	default:
		err = e.ds.PutStamped(v, stamp)
	}
	e.after(ctx, err, key, false, undo)
	return err
}

// Update changes the document key with fn, under the store's lock.
func (e *docEngine[T]) Update(ctx context.Context, key string, fn func(*T) error) (T, error) {
	undo := e.before(ctx, key)
	v, err := e.ds.UpdateStamped(key, e.stamp(ctx), fn)
	e.after(ctx, err, key, false, undo)
	return v, err
}

// Delete removes the document key.
func (e *docEngine[T]) Delete(ctx context.Context, key string) error {
	undo := e.before(ctx, key)
	err := e.ds.Delete(key)
	e.after(ctx, err, key, true, undo)
	return err
}

// Watch registers the store's hooks: the engine tells them each write it
// made, once it stands — at the commit, in a transaction.
func (e *docEngine[T]) Watch(onWrite, onDelete func(key string)) {
	e.onWrite, e.onDelete = onWrite, onDelete
}

// keyOf is v's key, "" when the key function panics: the document store
// says why the write failed.
func (e *docEngine[T]) keyOf(v T) (key string) {
	defer func() {
		if recover() != nil {
			key = ""
		}
	}()
	return e.key(v)
}

// before is what writes back the value under key, when ctx runs in a
// transaction of the data's: the entity it holds now, or its absence; nil
// outside one. Nothing else writes the data meanwhile: the transaction holds
// the writer turn.
//
// On a store that keeps versions (ADR 0007 §3) the write back is a version
// of its own — the document store numbers every write, and never gives a
// number twice —, kit's, by nobody, and it prunes nothing; the versions the
// rolled back writes made are then taken out of the history, and the one
// they found holds what it held again — a write in place changed it
// (forget) —, so that what never committed never shows. The versions those writes pruned
// stay pruned, and an entity whose deletion is rolled back comes back
// without its versions, which the deletion took (kitsunium/sdk ADR 0143,
// deferred: a key's document and versions restored together).
func (e *docEngine[T]) before(ctx context.Context, key string) func(context.Context) error {
	if key == "" || !inLocal(ctx) {
		return nil
	}
	prev, err := e.ds.Get(key)
	switch {
	case err == nil:
		head := e.head(key)
		return func(context.Context) error {
			e.undoing.Store(key, true)
			defer e.undoing.Delete(key)
			err := e.ds.Put(prev)
			e.tell(key, false, err)
			if err != nil && !errors.Is(err, docstore.WriteUnconfirmed) {
				return err
			}
			return errors.Join(err, e.forget(key, head, prev))
		}
	case errors.Is(err, docstore.DocumentNotFound):
		return func(context.Context) error {
			err := e.ds.Delete(key)
			if errors.Is(err, docstore.DocumentNotFound) {
				return nil
			}
			e.tell(key, true, err)
			return err
		}
	}
	// What the key holds no longer decodes: it cannot be written back.
	return func(context.Context) error { return err }
}

// after keeps, once a write stands, what writes back what it replaced, and
// tells the store's hooks — at the commit, in a transaction.
func (e *docEngine[T]) after(ctx context.Context, err error, key string, deleted bool, undo func(context.Context) error) {
	if err != nil && !errors.Is(err, docstore.WriteUnconfirmed) {
		return
	}
	if undo != nil {
		recordUndo(ctx, undoStep{store: e.name, restore: undo})
	}
	fn := e.onWrite
	if deleted {
		fn = e.onDelete
	}
	if fn == nil {
		return
	}
	if !hold(ctx, heldEffect{release: func() error { fn(key); return nil }, inside: true}) {
		fn(key)
	}
}

// tell tells the store's hooks that a rollback wrote key back: what the
// write woke reads it anew.
func (e *docEngine[T]) tell(key string, deleted bool, err error) {
	if err != nil && !errors.Is(err, docstore.WriteUnconfirmed) {
		return
	}
	fn := e.onWrite
	if deleted {
		fn = e.onDelete
	}
	if fn != nil {
		fn(key)
	}
}

// Close closes the document store.
func (e *docEngine[T]) Close() error { return e.ds.Close() }

// Fold rewrites the store's log with only what it holds now.
func (e *docEngine[T]) Fold(_ context.Context) error { return e.ds.Fold() }
