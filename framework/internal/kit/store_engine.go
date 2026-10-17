package kit

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/data/vfs"
)

// A store is a port: Service.Store[T] is what a service declares and calls,
// its API and its refusals are kit's, and the backend it runs on is the
// app's choice (ADR 0004). plug.StoreEngine is that backend, as kit sees it:
// one engine over documents (ADR 0179 §2), compiled once whatever the
// entity type.
//
// A store holds one while its app runs, opened by the app's placement of
// the store: in memory or in the data directory — the SDK's document store
// (docEngine) —, or on a database — the SDK's document store over SQL
// (sqlEngine, store_sql.go) —, behind kit's sealing (seal_store.go) and its
// history (history.go) when the store needs them.
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
//
// A document an engine returns holds the record as the store's caller reads
// it — opened, for a store that seals —, and its entity when the engine
// decoded it; Entries are the records as they rest.

// storeFS is where a document store keeps its files: the data directory and
// the store's snapshot in it. A nil FS keeps the store in memory.
type storeFS struct {
	fs   vfs.FullFS
	path string
}

// docEngine is a store on the SDK's document store: in memory, or one
// snapshot and the small files written since in the data directory. The
// document store keeps every record in memory, persists each write before
// returning, and keeps the indexes in step with the records.
//
// The data directory and memory have no transactions: in one of kit's
// (transact.go), a write records the value it replaces, which a rollback
// writes back, and its hooks wait for the commit.
type docEngine struct {
	ds  *docstore.Store[docRecord]
	cfg *engineConfig
	// load keeps the first record the document store's load could not
	// decode: it refuses the start.
	load docLoad
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
// A record the load could not decode refuses it, whatever the document
// store made of it.
func openDocEngine(cfg *engineConfig, where storeFS, vs versionsOn) (*docEngine, error) {
	e := &docEngine{cfg: cfg, name: where.path, keep: vs.keep}
	dcfg := docstore.Config[docRecord]{Key: e.keyOf, FS: where.fs, Path: where.path}
	if vs.keep > 0 {
		dcfg.Versions, dcfg.Clock = vs.keep, vs.clock
		// Asked under the writers' lock by a write that would prune: it may
		// read this store, and kit's holds. A rollback's write back prunes
		// nothing: what it takes out of the history is its own (forget).
		dcfg.Held = func(key string) bool {
			if _, undoing := e.undoing.Load(key); undoing {
				return true
			}
			return vs.held != nil && vs.held(context.Background(), key)
		}
	}
	ds, err := docstore.Open(dcfg, cfg.specs(&e.load)...)
	if lerr := e.load.loaded(); lerr != nil {
		// What the load could not decode says why better than what the
		// document store made of it: a record that does not decode, rather
		// than one under another key.
		if err == nil {
			return nil, errors.Join(lerr, ds.Close())
		}
		return nil, lerr
	}
	if err != nil {
		return nil, err
	}
	e.ds = ds
	return e, nil
}

// keyOf is the document store's key function.
func (e *docEngine) keyOf(r docRecord) string { return e.cfg.keyOf(r, &e.load) }

// Get reads the document key.
//
//ktn:allow-unused-param: the StoreEngine interface passes a context the local document store does not take
func (e *docEngine) Get(_ context.Context, key string) (plug.DocumentValue, error) {
	r, err := e.ds.Get(key)
	return r.value(), err
}

// List reads every document.
//
//ktn:allow-unused-param: the StoreEngine interface passes a context the local document store does not take
func (e *docEngine) List(_ context.Context) ([]plug.DocumentValue, error) {
	rs, err := e.ds.List()
	if err != nil {
		return nil, err
	}
	return values(rs), nil
}

// Filter reads the documents keep accepts.
func (e *docEngine) Filter(ctx context.Context, keep func(d plug.DocumentValue) bool) ([]plug.DocumentValue, error) {
	all, err := e.List(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, d := range all {
		if keep(d) {
			out = append(out, d)
		}
	}
	return out, nil
}

// Lookup reads the document a unique index names by key.
//
//ktn:allow-unused-param: the StoreEngine interface passes a context the local document store does not take
func (e *docEngine) Lookup(_ context.Context, index, key string) (plug.DocumentValue, error) {
	r, err := e.ds.Lookup(index, key)
	return r.value(), err
}

// Find reads every document an index names by key.
//
//ktn:allow-unused-param: the StoreEngine interface passes a context the local document store does not take
func (e *docEngine) Find(_ context.Context, index, key string) ([]plug.DocumentValue, error) {
	rs, err := e.ds.Find(index, key)
	if err != nil {
		return nil, err
	}
	return values(rs), nil
}

// Count is how many documents the store holds.
//
//ktn:allow-unused-param: the StoreEngine interface passes a context the local document store does not take
func (e *docEngine) Count(_ context.Context) (int, error) { return e.ds.Stats().Documents, nil }

// KeyedEntries are up to limit records as the store keeps them, with
// their keys: what a sealing store reads its records by (seal_store.go).
//
//ktn:allow-unused-param: the entriesSource interface passes a context the local document store does not take
func (e *docEngine) KeyedEntries(_ context.Context, limit int) ([]docstore.Entry, error) {
	return e.ds.Entries(limit)
}

// Entries are the latest limit documents, raw.
//
//ktn:allow-unused-param: the StoreEngine interface passes a context the local document store does not take
func (e *docEngine) Entries(_ context.Context, limit int) ([]json.RawMessage, error) {
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

// Write writes d in mode.
func (e *docEngine) Write(ctx context.Context, d plug.DocumentValue, mode plug.WriteMode) error {
	r := recordFor(d)
	key := e.writtenKey(r)
	undo := e.before(ctx, key)
	stamp := e.stamp(ctx)
	var err error
	switch mode {
	case plug.WriteInsert:
		err = e.ds.InsertStamped(r, stamp)
	case plug.WriteReplace:
		err = e.ds.ReplaceStamped(r, stamp)
	case plug.WriteUpsert:
		err = e.ds.PutStamped(r, stamp)
	default:
		err = e.ds.PutStamped(r, stamp)
	}
	e.after(ctx, err, key, false, undo)
	return err
}

// Update changes the document key with fn, under the store's lock.
func (e *docEngine) Update(ctx context.Context, key string, fn func(d *plug.DocumentValue) error) (plug.DocumentValue, error) {
	undo := e.before(ctx, key)
	r, err := e.ds.UpdateStamped(key, e.stamp(ctx), func(r *docRecord) error {
		d := r.value()
		if err := fn(&d); err != nil {
			return err
		}
		*r = recordFor(d)
		return nil
	})
	e.after(ctx, err, key, false, undo)
	return r.value(), err
}

// Delete removes the document key.
func (e *docEngine) Delete(ctx context.Context, key string) error {
	undo := e.before(ctx, key)
	err := e.ds.Delete(key)
	e.after(ctx, err, key, true, undo)
	return err
}

// Watch registers the store's hooks: the engine tells them each write it
// made, once it stands — at the commit, in a transaction.
func (e *docEngine) Watch(onWrite, onDelete func(key string)) {
	e.onWrite, e.onDelete = onWrite, onDelete
}

// writtenKey is r's key, "" when the key function panics: the document
// store says why the write failed.
func (e *docEngine) writtenKey(r docRecord) (key string) {
	defer func() {
		if recover() != nil {
			key = ""
		}
	}()
	return e.keyOf(r)
}

// before is what writes back the value under key, when ctx runs in a
// transaction of the data's: the record it holds now, or its absence; nil
// outside one. Nothing else writes the data meanwhile: the transaction holds
// the writer turn.
//
// On a store that keeps versions (ADR 0007 §3) the write back is a version
// of its own — the document store numbers every write, and never gives a
// number twice —, kit's, by nobody, and it prunes nothing; the versions the
// rolled back writes made are then taken out of the history, and the one
// they found holds what it held again — a write in place changed it
// (forget) —, so that what never committed never shows. The versions those writes pruned
// stay pruned, and a record whose deletion is rolled back comes back
// without its versions, which the deletion took (kitsunium/sdk ADR 0143,
// deferred: a key's document and versions restored together).
func (e *docEngine) before(ctx context.Context, key string) func(context.Context) error {
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
		return e.uncreate(key)
	}
	// What the key holds can no longer be read: it cannot be written back.
	return func(context.Context) error { return err }
}

// uncreate is what takes back a write that finds no document under key: its
// deletion — or, over a record in the trash, which the write takes out
// (ADR 0178 C4), the record put back in, where an earlier trash's own
// rollback finds it to restore. The version the write made stays: a number
// is never given twice.
func (e *docEngine) uncreate(key string) func(context.Context) error {
	if e.trashedNow(key) {
		return func(context.Context) error { return e.unconfirmed(e.ds.Trash(key), key, true) }
	}
	return func(context.Context) error {
		err := e.ds.Delete(key)
		if errors.Is(err, docstore.DocumentNotFound) {
			return nil
		}
		e.tell(key, true, err)
		return err
	}
}

// after keeps, once a write stands, what writes back what it replaced, and
// tells the store's hooks — at the commit, in a transaction.
func (e *docEngine) after(ctx context.Context, err error, key string, deleted bool, undo func(context.Context) error) {
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
	if !hold(ctx, heldEffect{release: func(context.Context) error { fn(key); return nil }, inside: true}) {
		fn(key)
	}
}

// tell tells the store's hooks that a rollback wrote key back: what the
// write woke reads it anew.
func (e *docEngine) tell(key string, deleted bool, err error) {
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
func (e *docEngine) Close() error { return e.ds.Close() }

// Fold rewrites the store's log with only what it holds now.
//
//ktn:allow-unused-param: the Folder interface passes a context the local document store does not take
func (e *docEngine) Fold(_ context.Context) error { return e.ds.Fold() }
