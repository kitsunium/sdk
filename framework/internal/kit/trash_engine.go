package kit

import (
	"context"
	"errors"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
)

// The trash as the store's engines keep it (trash.go, ADR 0178 C4): the
// SDK's document store opened with versions, in memory, in files or over
// SQL, and the engines that wrap one, which hand the calls on — the
// siblings of plug.StoreEngine that plug.Trasher names.

// pluggedTombstones are the SDK's tombstones as the store's engine hands
// them on.
func pluggedTombstones(stones []docstore.Tombstone) []plug.TombstoneValue {
	if stones == nil {
		return nil
	}
	out := make([]plug.TombstoneValue, len(stones))
	for i, st := range stones {
		out[i] = plug.TombstoneValue{Key: st.Key, At: st.At}
	}
	return out
}

// Trash moves the document key to the trash. In a transaction of the data's,
// a rollback brings it back, its versions with it.
func (e *docEngine) Trash(ctx context.Context, key string) error {
	var undo func(context.Context) error
	if key != "" && inLocal(ctx) {
		undo = func(context.Context) error { return e.unconfirmed(e.ds.Restore(key), key, false) }
	}
	err := e.ds.Trash(key)
	e.after(ctx, err, key, true, undo)
	return err
}

// Untrash brings the document key back from the trash. In a transaction of
// the data's, a rollback trashes it again.
func (e *docEngine) Untrash(ctx context.Context, key string) error {
	var undo func(context.Context) error
	if key != "" && inLocal(ctx) {
		undo = func(context.Context) error { return e.unconfirmed(e.ds.Trash(key), key, true) }
	}
	err := e.ds.Restore(key)
	e.after(ctx, err, key, false, undo)
	return err
}

// Purge empties the trash of the document key: no rollback brings it back.
//
//ktn:allow-unused-param: the Trasher interface passes a context the local document store does not take
func (e *docEngine) Purge(_ context.Context, key string) error { return e.ds.Purge(key) }

// Tombstones are the documents in the trash.
//
//ktn:allow-unused-param: the Trasher interface passes a context the local document store does not take
func (e *docEngine) Tombstones(_ context.Context) ([]plug.TombstoneValue, error) {
	stones, err := e.ds.Tombstones()
	return pluggedTombstones(stones), err
}

// unconfirmed tells the store's hooks of a rollback's write back of key, and
// returns its error but an unconfirmed write, which stands.
func (e *docEngine) unconfirmed(err error, key string, deleted bool) error {
	e.tell(key, deleted, err)
	if err != nil && !errors.Is(err, docstore.WriteUnconfirmed) {
		return err
	}
	return nil
}

// trashedNow reports whether a document is in the trash under key: its
// versions read while no document is stored.
func (e *docEngine) trashedNow(key string) bool {
	if e.keep == 0 {
		return false
	}
	_, err := e.ds.Versions(key)
	return err == nil
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
	return err
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
	return err
}

// Purge empties the trash of the record key, in the transaction ctx carries.
func (e *sqlEngine) Purge(ctx context.Context, key string) error {
	c, done, err := e.call(ctx, true)
	if err != nil {
		return err
	}
	defer done()
	return e.ds.Purge(c, key)
}

// Tombstones are the records in the trash.
func (e *sqlEngine) Tombstones(ctx context.Context) ([]plug.TombstoneValue, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return nil, err
	}
	defer done()
	stones, err := e.ds.Tombstones(c)
	return pluggedTombstones(stones), err
}

// trashOf is the trash eng keeps, false for an engine that keeps none.
func trashOf(eng plug.StoreEngine) (plug.Trasher, bool) {
	tr, ok := eng.(plug.Trasher)
	return tr, ok
}

// Trash moves the record key to the trash; its former values stay, for a
// restoration.
func (h *historied) Trash(ctx context.Context, key string) error {
	tr, ok := trashOf(h.StoreEngine)
	if !ok {
		return docstore.VersionsNotKept
	}
	return tr.Trash(ctx, key)
}

// Untrash brings the record key back, its former values with it.
func (h *historied) Untrash(ctx context.Context, key string) error {
	tr, ok := trashOf(h.StoreEngine)
	if !ok {
		return docstore.VersionsNotKept
	}
	return tr.Untrash(ctx, key)
}

// Purge empties the trash of the record key, then removes its former
// values, in one transaction on a database.
func (h *historied) Purge(ctx context.Context, key string) error {
	tr, ok := trashOf(h.StoreEngine)
	if !ok {
		return docstore.VersionsNotKept
	}
	return h.inOne(ctx, func(ctx context.Context) error {
		err := tr.Purge(ctx, key)
		if h.hist == nil || (err != nil && !errors.Is(err, docstore.WriteUnconfirmed)) {
			return err
		}
		return errors.Join(err, h.remove(ctx, key))
	})
}

// Tombstones are the records in the trash.
func (h *historied) Tombstones(ctx context.Context) ([]plug.TombstoneValue, error) {
	tr, ok := trashOf(h.StoreEngine)
	if !ok {
		return nil, docstore.VersionsNotKept
	}
	return tr.Tombstones(ctx)
}

// Trash moves the record key to the trash, sealed as it rests: its own data
// key stays, for a restoration.
func (e *sealedEngine) Trash(ctx context.Context, key string) error {
	tr, ok := trashOf(e.inner)
	if !ok {
		return docstore.VersionsNotKept
	}
	return tr.Trash(ctx, key)
}

// Untrash brings the record key back, sealed as it rested.
func (e *sealedEngine) Untrash(ctx context.Context, key string) error {
	tr, ok := trashOf(e.inner)
	if !ok {
		return docstore.VersionsNotKept
	}
	return tr.Untrash(ctx, key)
}

// Purge empties the trash of the record key, then destroys its own data
// key, when it had one — at the commit, in a transaction —, as Delete does.
func (e *sealedEngine) Purge(ctx context.Context, key string) error {
	tr, ok := trashOf(e.inner)
	if !ok {
		return docstore.VersionsNotKept
	}
	err := tr.Purge(ctx, key)
	if (err != nil && !errors.Is(err, docstore.WriteUnconfirmed)) || !e.s.plan().sealsAny() {
		return err
	}
	e.dropOwnKeyAfter(ctx, key)
	return err
}

// Tombstones are the records in the trash.
func (e *sealedEngine) Tombstones(ctx context.Context) ([]plug.TombstoneValue, error) {
	tr, ok := trashOf(e.inner)
	if !ok {
		return nil, docstore.VersionsNotKept
	}
	return tr.Tombstones(ctx)
}
