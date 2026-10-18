package storeengine

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"slices"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/data/vfs"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// fileEngine is a store on the SDK's document store: in memory, or one
// snapshot and the small files written since in the data directory. The
// document store keeps every record in memory, persists each write before
// returning, and keeps the indexes in step with the records.
//
// The data directory and memory have no transactions: in one of kit's, a
// write records the value it replaces, which a rollback writes back, and its
// hooks wait for the commit (plug.Transaction).
type fileEngine struct {
	ds   *docstore.Store[docRecord]
	docs *documents
	// undoing are the keys a rollback writes back, whose versions no write
	// prunes meanwhile (before).
	undoing sync.Map
	// onWrite and onDelete are what Watch registered.
	onWrite, onDelete func(key string)
}

// openFile opens the document store of a store: loaded from where cfg says
// — the snapshot, what was written since replayed on it, the indexes
// rebuilt — or empty in memory; keeping each record's versions as cfg says.
// A record the load could not decode refuses it, whatever the document
// store made of it.
func openFile(cfg *plug.StoreConfig) (*fileEngine, error) {
	e := &fileEngine{docs: &documents{cfg: cfg}}
	dcfg := docstore.Config[docRecord]{Key: e.docs.keyOf, Path: cfg.Path}
	if cfg.Data != nil {
		fsys, ok := cfg.Data.(vfs.FullFS)
		if !ok {
			return nil, plug.Refuse(plug.RefusalLoad, errs.Wrap(docstore.LoadFailed, errs.WrapParams{},
				errs.String("problem", "the data directory is not writable")))
		}
		dcfg.FS = fsys
	}
	if cfg.Versions > 0 {
		dcfg.Versions, dcfg.Clock = cfg.Versions, clockOf(cfg.Clock)
		// Asked under the writers' lock by a write that would prune: it may
		// read this store, and kit's holds. A rollback's write back prunes
		// nothing: what it takes out of the history is its own (forget).
		dcfg.Held = func(key string) bool {
			if _, undoing := e.undoing.Load(key); undoing {
				return true
			}
			return cfg.Held != nil && cfg.Held(context.Background(), key)
		}
	}
	ds, err := docstore.Open(dcfg, e.docs.specs()...)
	if lerr := e.docs.load.loaded(); lerr != nil {
		// What the load could not decode says why better than what the
		// document store made of it: a record that does not decode, rather
		// than one under another key.
		if err == nil {
			return nil, errors.Join(lerr, ds.Close())
		}
		return nil, lerr
	}
	if err != nil {
		return nil, said(err)
	}
	e.ds = ds
	return e, nil
}

// keep is how many former versions the document store keeps of each record
// (ADR 0007 §3): none when zero.
func (e *fileEngine) keep() int { return e.docs.cfg.Versions }

// Get reads the document key.
//
//ktn:allow-unused-param: the StoreEngine interface passes a context the local document store does not take
func (e *fileEngine) Get(_ context.Context, key string) (plug.DocumentValue, error) {
	r, err := e.ds.Get(key)
	return r.value(), said(err)
}

// List reads every document.
//
//ktn:allow-unused-param: the StoreEngine interface passes a context the local document store does not take
func (e *fileEngine) List(_ context.Context) ([]plug.DocumentValue, error) {
	rs, err := e.ds.List()
	if err != nil {
		return nil, said(err)
	}
	return values(rs), nil
}

// Filter reads the documents keep accepts.
func (e *fileEngine) Filter(ctx context.Context, keep func(d plug.DocumentValue) bool) ([]plug.DocumentValue, error) {
	all, err := e.List(ctx)
	if err != nil {
		return nil, err
	}
	return filtered(all, keep), nil
}

// Lookup reads the document a unique index names by key.
//
//ktn:allow-unused-param: the StoreEngine interface passes a context the local document store does not take
func (e *fileEngine) Lookup(_ context.Context, index, key string) (plug.DocumentValue, error) {
	r, err := e.ds.Lookup(index, key)
	return r.value(), said(err)
}

// Find reads every document an index names by key.
//
//ktn:allow-unused-param: the StoreEngine interface passes a context the local document store does not take
func (e *fileEngine) Find(_ context.Context, index, key string) ([]plug.DocumentValue, error) {
	rs, err := e.ds.Find(index, key)
	if err != nil {
		return nil, said(err)
	}
	return values(rs), nil
}

// Count is how many documents the store holds.
//
//ktn:allow-unused-param: the StoreEngine interface passes a context the local document store does not take
func (e *fileEngine) Count(_ context.Context) (int, error) { return e.ds.Stats().Documents, nil }

// KeyedEntries are up to limit records as the store keeps them, with
// their keys: what a sealing store reads its records by.
//
//ktn:allow-unused-param: the EntrySource interface passes a context the local document store does not take
func (e *fileEngine) KeyedEntries(_ context.Context, limit int) ([]plug.EntryValue, error) {
	entries, err := e.ds.Entries(limit)
	if err != nil {
		return nil, said(err)
	}
	return pluggedEntries(entries), nil
}

// Entries are the latest limit documents, raw.
func (e *fileEngine) Entries(ctx context.Context, limit int) ([]json.RawMessage, error) {
	entries, err := e.KeyedEntries(ctx, limit)
	if err != nil {
		return nil, err
	}
	return raws(entries), nil
}

// Write writes d in mode.
func (e *fileEngine) Write(ctx context.Context, d plug.DocumentValue, mode plug.WriteMode) error {
	r := recordFor(d)
	key := e.docs.writtenKey(r)
	undo := e.before(ctx, key)
	stamp := e.docs.stamp(ctx)
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
	return said(err)
}

// Update changes the document key with fn, under the store's lock.
func (e *fileEngine) Update(ctx context.Context, key string, fn func(d *plug.DocumentValue) error) (plug.DocumentValue, error) {
	undo := e.before(ctx, key)
	r, err := e.ds.UpdateStamped(key, e.docs.stamp(ctx), func(r *docRecord) error {
		d := r.value()
		if err := fn(&d); err != nil {
			return err
		}
		*r = recordFor(d)
		return nil
	})
	e.after(ctx, err, key, false, undo)
	return r.value(), said(err)
}

// Delete removes the document key.
func (e *fileEngine) Delete(ctx context.Context, key string) error {
	undo := e.before(ctx, key)
	err := e.ds.Delete(key)
	e.after(ctx, err, key, true, undo)
	return said(err)
}

// Watch registers the store's hooks: the engine tells them each write it
// made, once it stands — at the commit, in a transaction.
func (e *fileEngine) Watch(onWrite, onDelete func(key string)) {
	e.onWrite, e.onDelete = onWrite, onDelete
}

// local reports whether ctx runs in a transaction of the data's.
func (e *fileEngine) local(ctx context.Context) bool {
	tx := e.docs.cfg.Transaction
	return tx != nil && tx.Local(ctx)
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
// (forget) —, so that what never committed never shows. The versions those
// writes pruned stay pruned, and a record whose deletion is rolled back comes
// back without its versions, which the deletion took (kitsunium/sdk ADR
// 0143, deferred: a key's document and versions restored together).
func (e *fileEngine) before(ctx context.Context, key string) func(context.Context) error {
	if key == "" || !e.local(ctx) {
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
				return said(err)
			}
			return said(errors.Join(err, e.forget(key, head, prev)))
		}
	case errors.Is(err, docstore.DocumentNotFound):
		return e.uncreate(key)
	}
	// What the key holds can no longer be read: it cannot be written back.
	return func(context.Context) error { return said(err) }
}

// uncreate is what takes back a write that finds no document under key: its
// deletion — or, over a record in the trash, which the write takes out
// (ADR 0178 C4), the record put back in, where an earlier trash's own
// rollback finds it to restore. The version the write made stays: a number
// is never given twice.
func (e *fileEngine) uncreate(key string) func(context.Context) error {
	if e.trashedNow(key) {
		return func(context.Context) error { return e.unconfirmed(e.ds.Trash(key), key, true) }
	}
	return func(context.Context) error {
		err := e.ds.Delete(key)
		if errors.Is(err, docstore.DocumentNotFound) {
			return nil
		}
		e.tell(key, true, err)
		return said(err)
	}
}

// after keeps, once a write stands, what writes back what it replaced, and
// tells the store's hooks — at the commit, in a transaction.
func (e *fileEngine) after(ctx context.Context, err error, key string, deleted bool, undo func(context.Context) error) {
	if err != nil && !errors.Is(err, docstore.WriteUnconfirmed) {
		return
	}
	tx := e.docs.cfg.Transaction
	if undo != nil && tx != nil {
		tx.Undo(ctx, e.docs.cfg.Name, undo)
	}
	fn := e.onWrite
	if deleted {
		fn = e.onDelete
	}
	if fn == nil {
		return
	}
	if tx == nil || !tx.AtCommit(ctx, func() { fn(key) }) {
		fn(key)
	}
}

// tell tells the store's hooks that a rollback wrote key back: what the
// write woke reads it anew.
func (e *fileEngine) tell(key string, deleted bool, err error) {
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
func (e *fileEngine) Close() error { return said(e.ds.Close()) }

// Fold rewrites the store's log with only what it holds now.
//
//ktn:allow-unused-param: the Folder interface passes a context the local document store does not take
func (e *fileEngine) Fold(_ context.Context) error { return said(e.ds.Fold()) }

// Keys lists the document store's keys above after: in memory, without a
// document copied.
//
//ktn:allow-unused-param: the Keyser interface passes a context the local document store does not take
func (e *fileEngine) Keys(_ context.Context, after string) iter.Seq2[string, error] {
	return saidKeys(e.ds.Keys(after))
}

// StoredVersions are the versions of the record under key as they rest.
//
//ktn:allow-unused-param: the VersionKeeper interface passes a context the local document store does not take
func (e *fileEngine) StoredVersions(_ context.Context, key string) ([]plug.VersionValue, error) {
	vs, err := e.ds.Versions(key)
	return pluggedVersions(vs), said(err)
}

// RewriteVersions rewrites the former versions of the record under key
// with fn, under the writers' lock.
//
//ktn:allow-unused-param: the VersionKeeper interface passes a context the local document store does not take
func (e *fileEngine) RewriteVersions(_ context.Context, key string, fn func(vs []plug.VersionValue) ([]plug.VersionValue, error)) error {
	return said(e.ds.RewriteVersions(key, rewritingSDK(fn)))
}

// head is the number of the current version of the record under key,
// before a write of a transaction replaces it: a rollback takes every
// version above it out of the history. Zero on a store that keeps no
// versions.
func (e *fileEngine) head(key string) uint64 {
	if e.keep() == 0 {
		return 0
	}
	all, err := e.ds.Versions(key)
	if err != nil || len(all) == 0 {
		return 0
	}
	return all[0].Number
}

// forget takes out of the history of the record under key the versions a
// rolled back write made — every former version numbered above head, the
// version the write found — and gives head back the document it held, prev:
// a write in place, a workflow's transition of the state alone, changed it
// without a new number. What never committed never shows. The version the
// rollback made stays: a number is never given twice.
func (e *fileEngine) forget(key string, head uint64, prev docRecord) error {
	if e.keep() == 0 || head == 0 {
		return nil
	}
	held, merr := json.Marshal(prev)
	err := e.ds.RewriteVersions(key, func(former []docstore.Version) ([]docstore.Version, error) {
		former = slices.DeleteFunc(former, func(v docstore.Version) bool { return v.Number > head })
		for i := range former {
			if former[i].Number == head && merr == nil {
				former[i].JSON = held
			}
		}
		return former, nil
	})
	if errors.Is(err, docstore.WriteUnconfirmed) {
		return nil
	}
	return err
}

// VersionWeight is what the document store's versions weigh, from the
// counter its writes move.
//
//ktn:allow-unused-param: the VersionWeighter interface passes a context the local document store does not take
func (e *fileEngine) VersionWeight(_ context.Context) (plug.VersionWeightValue, error) {
	return pluggedWeight(e.ds.VersionWeight())
}

// Trash moves the document key to the trash. In a transaction of the data's,
// a rollback brings it back, its versions with it.
func (e *fileEngine) Trash(ctx context.Context, key string) error {
	var undo func(context.Context) error
	if key != "" && e.local(ctx) {
		undo = func(context.Context) error { return e.unconfirmed(e.ds.Restore(key), key, false) }
	}
	err := e.ds.Trash(key)
	e.after(ctx, err, key, true, undo)
	return said(err)
}

// Untrash brings the document key back from the trash. In a transaction of
// the data's, a rollback trashes it again.
func (e *fileEngine) Untrash(ctx context.Context, key string) error {
	var undo func(context.Context) error
	if key != "" && e.local(ctx) {
		undo = func(context.Context) error { return e.unconfirmed(e.ds.Trash(key), key, true) }
	}
	err := e.ds.Restore(key)
	e.after(ctx, err, key, false, undo)
	return said(err)
}

// Purge empties the trash of the document key: no rollback brings it back.
//
//ktn:allow-unused-param: the Trasher interface passes a context the local document store does not take
func (e *fileEngine) Purge(_ context.Context, key string) error { return said(e.ds.Purge(key)) }

// Tombstones are the documents in the trash.
//
//ktn:allow-unused-param: the Trasher interface passes a context the local document store does not take
func (e *fileEngine) Tombstones(_ context.Context) ([]plug.TombstoneValue, error) {
	stones, err := e.ds.Tombstones()
	return pluggedTombstones(stones), said(err)
}

// unconfirmed tells the store's hooks of a rollback's write back of key, and
// returns its error but an unconfirmed write, which stands.
func (e *fileEngine) unconfirmed(err error, key string, deleted bool) error {
	e.tell(key, deleted, err)
	if err != nil && !errors.Is(err, docstore.WriteUnconfirmed) {
		return said(err)
	}
	return nil
}

// trashedNow reports whether a document is in the trash under key: its
// versions read while no document is stored.
func (e *fileEngine) trashedNow(key string) bool {
	if e.keep() == 0 {
		return false
	}
	_, err := e.ds.Versions(key)
	return err == nil
}

// Documents is the file store itself.
func (e *fileEngine) Documents() (plug.Documents, bool) { return e, true }

// KeyList yields the keys of the file store.
func (e *fileEngine) KeyList(ctx context.Context) iter.Seq2[string, error] { return e.Keys(ctx, "") }

// TombstoneList is the file store's trash.
func (e *fileEngine) TombstoneList(ctx context.Context) ([]plug.TombstoneValue, error) {
	if e.keep() == 0 {
		return nil, nil
	}
	return e.Tombstones(ctx)
}

// VersionList is the versions of the document key in the file store.
func (e *fileEngine) VersionList(ctx context.Context, key string) ([]plug.VersionValue, error) {
	return e.StoredVersions(ctx, key)
}

// Current is the document key as the file store keeps it.
//
//ktn:allow-unused-param: the Documents interface passes a context the local document store does not take
func (e *fileEngine) Current(_ context.Context, key string) (json.RawMessage, error) {
	r, err := e.ds.Get(key)
	if err != nil {
		return nil, said(err)
	}
	return r.raw, nil
}

// KeepsVersions reports whether the file store keeps versions.
func (e *fileEngine) KeepsVersions() bool { return e.keep() > 0 }

// PutDocument writes the document key into the file store with its
// versions and its trash as they are given.
//
//ktn:allow-unused-param: the DocumentSink interface passes a context the local document store does not take
func (e *fileEngine) PutDocument(_ context.Context, key string, versions []plug.VersionValue, trashedAt time.Time) error {
	return said(e.ds.PutDocument(key, sdkVersions(versions), trashedAt))
}

// InsertRaw writes a document, as it rests, into a file store that keeps no
// versions, keyed and indexed as the store keys and indexes it.
//
//ktn:allow-unused-param: the DocumentSink interface passes a context the local document store does not take
func (e *fileEngine) InsertRaw(_ context.Context, raw json.RawMessage) error {
	return said(e.ds.Insert(rawRecord(raw)))
}

// saidKeys is keys, its failure said in kit's terms.
func saidKeys(keys iter.Seq2[string, error]) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		for key, err := range keys {
			if !yield(key, said(err)) {
				return
			}
		}
	}
}
