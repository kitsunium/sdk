package storekit

import (
	"context"
	"encoding/json"
	"iter"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// Stores on SQL (ADR 0004, step 2). A store a database keeps is still a
// document store: the SDK's over SQL, a table per store — a row per entity,
// the entity as the JSON kit encoded, its index rows beside it in
// <table>___ix, kept in the same write —, on the database's pool, each call
// on the transaction its context carries for that database and within
// <name>-timeout (plug.StoreConfig.Call). Nothing is held in memory: the
// database is the source of truth. Index keys reach the table as keyed
// hashes: an e-mail, a token's hash, a subject never does.

// sqlKeysChunk is how many keys one call of a database's engine lists, each
// under its own <name>-timeout: a long listing is many short calls, never one
// call the timeout ends halfway.
const sqlKeysChunk int = 256

// sqlEngine is a store on a database: the SDK's document store over SQL,
// of records as they rest.
type sqlEngine struct {
	ds   *docstore.SQLStore[docRecord]
	docs *documents
	// onWrite is what the store's Watch registered: the SDK tells it each
	// write once its transaction commits, and a rollback of kit's
	// transaction tells it again, so that what reads the store reads it
	// anew.
	onWrite func(key string)
}

// openSQL opens the SDK's document store over the table cfg names, on the
// database cfg's scope is, of records as cfg keys and indexes them: a
// store's entities, the records a sealing store keeps of them, a store's
// history, a workflow's journal, kit's registry of its tables.
func openSQL(cfg *plug.StoreConfig) (*sqlEngine, error) {
	db, ok := cfg.Database.(*database)
	if !ok {
		return nil, plug.Refuse(plug.RefusalLoad, errs.Wrap(docstore.LoadFailed, errs.WrapParams{},
			errs.String("problem", "the store's database was not opened by the store subsystem")))
	}
	e := &sqlEngine{docs: &documents{cfg: cfg}}
	dcfg := docstore.SQLConfig[docRecord]{Key: e.docs.keyOf, Transactor: db.tm, Dialect: db.dialect, Table: cfg.Table, IndexKey: cfg.IndexKey}
	if cfg.Versions > 0 {
		// Held is asked inside the write's transaction, with its context: a
		// hold kit keeps on the same database is read in it.
		dcfg.Versions, dcfg.Clock, dcfg.Held = cfg.Versions, clockOf(cfg.Clock), cfg.Held
	}
	ds, err := docstore.OpenSQL(dcfg, e.docs.specs()...)
	if err != nil {
		return nil, said(err)
	}
	e.ds = ds
	return e, nil
}

// call is ctx for a call of the engine: on the transaction it carries for
// the store's database, else on the pool, within the database's timeout, as
// kit's core says; done ends it.
func (e *sqlEngine) call(ctx context.Context, write bool) (context.Context, func(), error) {
	if e.docs.cfg.Call == nil {
		return ctx, func() {}, nil
	}
	return e.docs.cfg.Call(ctx, write)
}

// keep is how many former versions the table keeps of each record (ADR
// 0007 §3), in <table>___vs: none when zero.
func (e *sqlEngine) keep() int { return e.docs.cfg.Versions }

// Get reads the record key.
func (e *sqlEngine) Get(ctx context.Context, key string) (plug.DocumentValue, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return plug.DocumentValue{}, err
	}
	defer done()
	r, err := e.ds.Get(c, key)
	return r.value(), said(err)
}

// List reads every record.
func (e *sqlEngine) List(ctx context.Context) ([]plug.DocumentValue, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return nil, err
	}
	defer done()
	rs, err := e.ds.List(c)
	if err != nil {
		return nil, said(err)
	}
	return values(rs), nil
}

// Filter reads the records keep keeps.
func (e *sqlEngine) Filter(ctx context.Context, keep func(d plug.DocumentValue) bool) ([]plug.DocumentValue, error) {
	all, err := e.List(ctx)
	if err != nil {
		return nil, err
	}
	return filtered(all, keep), nil
}

// Lookup reads the record whose unique index holds key.
func (e *sqlEngine) Lookup(ctx context.Context, index, key string) (plug.DocumentValue, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return plug.DocumentValue{}, err
	}
	defer done()
	r, err := e.ds.Lookup(c, index, key)
	return r.value(), said(err)
}

// Find reads the records an index files under key.
func (e *sqlEngine) Find(ctx context.Context, index, key string) ([]plug.DocumentValue, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return nil, err
	}
	defer done()
	rs, err := e.ds.Find(c, index, key)
	if err != nil {
		return nil, said(err)
	}
	return values(rs), nil
}

// Count is how many records the table holds.
func (e *sqlEngine) Count(ctx context.Context) (int, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return 0, err
	}
	defer done()
	n, err := e.ds.Count(c)
	return n, said(err)
}

// Entries are up to limit records as the table keeps them.
func (e *sqlEngine) Entries(ctx context.Context, limit int) ([]json.RawMessage, error) {
	entries, err := e.KeyedEntries(ctx, limit)
	if err != nil {
		return nil, err
	}
	return raws(entries), nil
}

// KeyedEntries are up to limit records as the table keeps them, with their
// keys: what a sealing store reads its records by.
func (e *sqlEngine) KeyedEntries(ctx context.Context, limit int) ([]plug.EntryValue, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return nil, err
	}
	defer done()
	entries, err := e.ds.Entries(c, limit)
	if err != nil {
		return nil, said(err)
	}
	return pluggedEntries(entries), nil
}

// Write writes d in mode, stamped as the caller's context says.
func (e *sqlEngine) Write(ctx context.Context, d plug.DocumentValue, mode plug.WriteMode) error {
	stamp := e.docs.stamp(ctx)
	c, done, err := e.call(ctx, true)
	if err != nil {
		return err
	}
	defer done()
	r := recordFor(d)
	switch mode {
	case plug.WriteInsert:
		err = e.ds.InsertStamped(c, r, stamp)
	case plug.WriteReplace:
		err = e.ds.ReplaceStamped(c, r, stamp)
	default:
		err = e.ds.PutStamped(c, r, stamp)
	}
	if err == nil {
		e.retold(ctx, e.docs.writtenKey(r))
	}
	return said(err)
}

// Update changes the record key with fn, in one write.
func (e *sqlEngine) Update(ctx context.Context, key string, fn func(d *plug.DocumentValue) error) (plug.DocumentValue, error) {
	stamp := e.docs.stamp(ctx)
	c, done, err := e.call(ctx, true)
	if err != nil {
		return plug.DocumentValue{}, err
	}
	defer done()
	r, err := e.ds.UpdateStamped(c, key, stamp, func(r *docRecord) error {
		d := r.value()
		if err := fn(&d); err != nil {
			return err
		}
		*r = recordFor(d)
		return nil
	})
	if err == nil {
		e.retold(ctx, key)
	}
	return r.value(), said(err)
}

// Delete removes the record key.
func (e *sqlEngine) Delete(ctx context.Context, key string) error {
	c, done, err := e.call(ctx, true)
	if err != nil {
		return err
	}
	defer done()
	err = e.ds.Delete(c, key)
	if err == nil {
		e.retold(ctx, key)
	}
	return said(err)
}

// retold keeps key for the store's hooks to hear again if kit's transaction
// rolls its write back: the SDK drops a rolled back write's hooks, and what
// the write woke — a workflow, a retention — must read the store anew.
func (e *sqlEngine) retold(ctx context.Context, key string) {
	tx := e.docs.cfg.Transaction
	if fn := e.onWrite; fn != nil && key != "" && tx != nil {
		tx.Retell(ctx, func() { fn(key) })
	}
}

// Watch registers the store's hooks with the SDK, which calls them once a
// write's transaction commits.
func (e *sqlEngine) Watch(onWrite, onDelete func(key string)) {
	e.onWrite = onWrite
	e.ds.OnWrite(onWrite)
	e.ds.OnDelete(onDelete)
}

// Close closes nothing: the pool is the database's.
func (e *sqlEngine) Close() error { return nil }

// Reindex files the table's index rows again: a record the refiling could
// not decode — or open — refuses it, whatever the SDK made of it.
func (e *sqlEngine) Reindex(ctx context.Context) error {
	err := e.ds.Reindex(ctx)
	if lerr := e.docs.load.loaded(); lerr != nil {
		return lerr
	}
	return said(err)
}

// Keys lists the table's keys above after, sqlKeysChunk at a time, each
// chunk one call on the transaction ctx carries for the database, else on
// the pool, within the database's timeout.
func (e *sqlEngine) Keys(ctx context.Context, after string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		for {
			chunk, err := e.keysChunk(ctx, after)
			if !yieldKeys(yield, chunk, err) || len(chunk) < sqlKeysChunk {
				return
			}
			after = chunk[len(chunk)-1]
		}
	}
}

// keysChunk is at most sqlKeysChunk keys above after, read in one call.
func (e *sqlEngine) keysChunk(ctx context.Context, after string) ([]string, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return nil, err
	}
	defer done()
	keys := make([]string, 0, sqlKeysChunk)
	for key, err := range e.ds.Keys(c, after) {
		if err != nil {
			return nil, said(err)
		}
		keys = append(keys, key)
		if len(keys) == sqlKeysChunk {
			break
		}
	}
	return keys, nil
}

// yieldKeys yields keys in order, then failure when there is one, and reports
// whether the caller wants more: false once it stopped, and after a failure,
// which is a listing's last yield, whatever the caller answered to it.
func yieldKeys(yield func(string, error) bool, keys []string, failure error) bool {
	for _, key := range keys {
		if !yield(key, nil) {
			return false
		}
	}
	if failure == nil {
		return true
	}
	return yield("", failure) && failure == nil
}

// StoredVersions are the versions of the record under key as the table
// keeps them.
func (e *sqlEngine) StoredVersions(ctx context.Context, key string) ([]plug.VersionValue, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return nil, err
	}
	defer done()
	vs, err := e.ds.Versions(c, key)
	return pluggedVersions(vs), said(err)
}

// RewriteVersions rewrites the former versions of the record under key
// with fn, in one write.
func (e *sqlEngine) RewriteVersions(ctx context.Context, key string, fn func(vs []plug.VersionValue) ([]plug.VersionValue, error)) error {
	c, done, err := e.call(ctx, true)
	if err != nil {
		return err
	}
	defer done()
	return said(e.ds.RewriteVersions(c, key, rewritingSDK(fn)))
}

// VersionWeight is what the table's versions weigh, in one statement on the
// transaction ctx carries for the database, else on the pool, within the
// database's timeout.
func (e *sqlEngine) VersionWeight(ctx context.Context) (plug.VersionWeightValue, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return plug.VersionWeightValue{}, err
	}
	defer done()
	return pluggedWeight(e.ds.VersionWeight(c))
}

// Trash moves the record key to the trash, in the transaction ctx carries.
func (e *sqlEngine) Trash(ctx context.Context, key string) error {
	c, done, err := e.call(ctx, true)
	if err != nil {
		return err
	}
	defer done()
	err = e.ds.Trash(c, key)
	if err == nil {
		e.retold(ctx, key)
	}
	return said(err)
}

// Untrash brings the record key back from the trash, in the transaction ctx
// carries.
func (e *sqlEngine) Untrash(ctx context.Context, key string) error {
	c, done, err := e.call(ctx, true)
	if err != nil {
		return err
	}
	defer done()
	err = e.ds.Restore(c, key)
	if err == nil {
		e.retold(ctx, key)
	}
	return said(err)
}

// Purge empties the trash of the record key, in the transaction ctx carries.
func (e *sqlEngine) Purge(ctx context.Context, key string) error {
	c, done, err := e.call(ctx, true)
	if err != nil {
		return err
	}
	defer done()
	return said(e.ds.Purge(c, key))
}

// Tombstones are the records in the trash.
func (e *sqlEngine) Tombstones(ctx context.Context) ([]plug.TombstoneValue, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return nil, err
	}
	defer done()
	stones, err := e.ds.Tombstones(c)
	return pluggedTombstones(stones), said(err)
}

// Documents is the table itself.
func (e *sqlEngine) Documents() (plug.Documents, bool) { return e, true }

// KeyList yields the keys of the table, a page at a time.
func (e *sqlEngine) KeyList(ctx context.Context) iter.Seq2[string, error] {
	return saidKeys(e.ds.Keys(ctx, ""))
}

// TombstoneList is the table's trash.
func (e *sqlEngine) TombstoneList(ctx context.Context) ([]plug.TombstoneValue, error) {
	if e.keep() == 0 {
		return nil, nil
	}
	stones, err := e.ds.Tombstones(ctx)
	return pluggedTombstones(stones), said(err)
}

// VersionList is the versions of the document key in the table.
func (e *sqlEngine) VersionList(ctx context.Context, key string) ([]plug.VersionValue, error) {
	vs, err := e.ds.Versions(ctx, key)
	return pluggedVersions(vs), said(err)
}

// Current is the document key as the table keeps it.
func (e *sqlEngine) Current(ctx context.Context, key string) (json.RawMessage, error) {
	r, err := e.ds.Get(ctx, key)
	if err != nil {
		return nil, said(err)
	}
	return r.raw, nil
}

// KeepsVersions reports whether the table keeps versions.
func (e *sqlEngine) KeepsVersions() bool { return e.keep() > 0 }

// PutDocument writes the document key into the table with its versions and
// its trash as they are given.
func (e *sqlEngine) PutDocument(ctx context.Context, key string, versions []plug.VersionValue, trashedAt time.Time) error {
	return said(e.ds.PutDocument(ctx, key, sdkVersions(versions), trashedAt))
}

// InsertRaw writes a document, as it rests, into a table that keeps no
// versions: the record as it rests — sealed members boxes for a store that
// seals —, keyed and indexed as the table's engine keys and indexes it.
func (e *sqlEngine) InsertRaw(ctx context.Context, raw json.RawMessage) error {
	return said(e.ds.Insert(ctx, rawRecord(raw)))
}
