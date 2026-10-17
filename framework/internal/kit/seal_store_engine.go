package kit

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// A store that seals, as kit's port sees it (ADR 0004, ADR 0006 §4): every
// call of plug.StoreEngine, its records opened where they are read and
// sealed where they are written (seal_store.go), and what kit's erasure and
// the privacy command ask of it beside the port.

// Get reads the record key, opened.
func (e *sealedEngine) Get(ctx context.Context, key string) (plug.DocumentValue, error) {
	d, err := e.inner.Get(ctx, key)
	if err != nil {
		return plug.DocumentValue{}, err
	}
	return e.opened(ctx, key, d)
}

// List reads every record, opened.
func (e *sealedEngine) List(ctx context.Context) ([]plug.DocumentValue, error) {
	entries, err := e.resting(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := make([]plug.DocumentValue, 0, len(entries))
	for _, en := range entries {
		d, err := e.opened(ctx, en.Key, plug.DocumentValue{JSON: en.JSON})
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// entriesSource is an engine that says the keys of the records it lists as
// they rest: the document store does. A sibling of the port.
type entriesSource interface {
	KeyedEntries(ctx context.Context, limit int) ([]docstore.Entry, error)
}

// resting are up to limit records as they rest, with their keys: the
// engine's, or read from the members kit does not seal.
func (e *sealedEngine) resting(ctx context.Context, limit int) ([]docstore.Entry, error) {
	if k, ok := e.inner.(entriesSource); ok {
		return k.KeyedEntries(ctx, limit)
	}
	all, err := e.inner.List(ctx)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	out := make([]docstore.Entry, len(all))
	for i, d := range all {
		out[i] = docstore.Entry{Key: e.docKey(d), JSON: d.JSON}
	}
	return out, nil
}

// Filter reads the records keep keeps, opened.
func (e *sealedEngine) Filter(ctx context.Context, keep func(d plug.DocumentValue) bool) ([]plug.DocumentValue, error) {
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

// Lookup reads the record whose unique index holds key, opened.
func (e *sealedEngine) Lookup(ctx context.Context, index, key string) (plug.DocumentValue, error) {
	d, err := e.inner.Lookup(ctx, index, key)
	if err != nil {
		return plug.DocumentValue{}, err
	}
	return e.openFound(ctx, d)
}

// Find reads the records an index files under key, opened.
func (e *sealedEngine) Find(ctx context.Context, index, key string) ([]plug.DocumentValue, error) {
	found, err := e.inner.Find(ctx, index, key)
	if err != nil {
		return nil, err
	}
	out := make([]plug.DocumentValue, 0, len(found))
	for _, d := range found {
		opened, err := e.openFound(ctx, d)
		if err != nil {
			return nil, err
		}
		out = append(out, opened)
	}
	return out, nil
}

// openFound opens a record an index found, its key read first.
func (e *sealedEngine) openFound(ctx context.Context, d plug.DocumentValue) (plug.DocumentValue, error) {
	return e.opened(ctx, e.restingKey(d.JSON), d)
}

// Count is how many records the store holds.
func (e *sealedEngine) Count(ctx context.Context) (int, error) { return e.inner.Count(ctx) }

// Entries are the records as they rest, sealed members as boxes: the Studio
// shows them sealed (classify_walk.go).
func (e *sealedEngine) Entries(ctx context.Context, limit int) ([]json.RawMessage, error) {
	return e.inner.Entries(ctx, limit)
}

// Write stores d as mode says. A record that may replace another is sealed
// under the store's writers' lock — through the engine's Update —, so that
// the data key it chooses is chosen after every write before it: a hold
// placed meanwhile, and the move of the record under its own key, are never
// undone by a write sealed before them. A Put whose key is new inserts, and
// replaces when another writer inserted it meanwhile.
func (e *sealedEngine) Write(ctx context.Context, d plug.DocumentValue, mode plug.WriteMode) error {
	key := e.s.keyOf(d.Value)
	if mode == insertOnly || key == "" || !e.s.plan().sealsAny() {
		return e.insert(ctx, d, mode)
	}
	var err error
	for range writeAttempts {
		err = e.replace(ctx, key, d)
		if mode == replaceOnly || !errors.Is(err, docstore.DocumentNotFound) {
			return err
		}
		if err = e.insert(ctx, d, insertOnly); !errors.Is(err, docstore.DocumentExists) {
			return err
		}
	}
	return err
}

// insert seals d and stores it as mode says, the engine checking what it
// finds under the key.
func (e *sealedEngine) insert(ctx context.Context, d plug.DocumentValue, mode plug.WriteMode) error {
	sealed, release, err := e.sealRecord(ctx, d)
	if err != nil {
		return err
	}
	defer release()
	return e.inner.Write(ctx, sealed, mode)
}

// replace stores d over the record under key, sealed under the store's
// writers' lock; the record it replaces is not opened — but on a store that
// keeps versions, which opens it to tell a change from none
// (revisions_engine.go).
func (e *sealedEngine) replace(ctx context.Context, key string, d plug.DocumentValue) error {
	if e.s.revisions > 0 && !inPlace(ctx) {
		_, err := e.keptUpdate(ctx, key, func(cur *plug.DocumentValue) error { *cur = d; return nil }, true)
		return err
	}
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()
	_, err := e.inner.Update(ctx, key, func(cur *plug.DocumentValue) error {
		next, rel, err := e.sealRecord(ctx, d)
		if err != nil {
			return err
		}
		release = rel
		*cur = next
		return nil
	})
	return err
}

// Update applies fn to the record under key, opened, and stores it sealed
// under the store's writers' lock. On a store that keeps versions, a record
// that means what it meant is kept as it rests, or sealed again in place
// (revisions_engine.go).
func (e *sealedEngine) Update(ctx context.Context, key string, fn func(d *plug.DocumentValue) error) (plug.DocumentValue, error) {
	if e.s.revisions > 0 && !inPlace(ctx) {
		return e.keptUpdate(ctx, key, fn, false)
	}
	return e.update(ctx, key, fn)
}

// update applies fn to the record under key, opened, and stores it sealed
// anew.
func (e *sealedEngine) update(ctx context.Context, key string, fn func(d *plug.DocumentValue) error) (plug.DocumentValue, error) {
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()
	var out plug.DocumentValue
	_, err := e.inner.Update(ctx, key, func(cur *plug.DocumentValue) error {
		d, err := e.opened(ctx, key, *cur)
		if err != nil {
			return err
		}
		if err := fn(&d); err != nil {
			return err
		}
		next, rel, err := e.sealRecord(ctx, d)
		if err != nil {
			return err
		}
		release = rel
		*cur, out = next, d
		return nil
	})
	if err != nil {
		return plug.DocumentValue{}, err
	}
	return out, nil
}

// Delete removes the record under key, then its own data key, when it had
// one: every copy of what that key sealed stops opening. Inside a
// transaction ([Transact]) the key goes once it commits: a rollback puts the
// record back, sealed under it.
func (e *sealedEngine) Delete(ctx context.Context, key string) error {
	err := e.inner.Delete(ctx, key)
	if (err != nil && !errors.Is(err, docstore.WriteUnconfirmed)) || !e.s.plan().sealsAny() {
		return err
	}
	e.dropOwnKeyAfter(ctx, key)
	return err
}

// dropOwnKeyAfter destroys the own data key of the record under key, gone
// — deleted, or purged from the trash —: at the commit, in a transaction,
// since a rollback puts the record back, sealed under it.
func (e *sealedEngine) dropOwnKeyAfter(ctx context.Context, key string) {
	drop := func(ctx context.Context) {
		if derr := e.dropOwnKey(ctx, key); derr != nil {
			logger.Warn(ctx, e.a.log, "a deleted record's own data key could not be destroyed",
				logger.String("store", e.s.id), logger.String("error", describeText(derr)))
		}
	}
	if !hold(ctx, heldEffect{node: e.s.id, inside: true, release: func(rctx context.Context) error {
		drop(context.WithoutCancel(rctx))
		return nil
	}}) {
		drop(ctx)
	}
}

// dropOwnKey destroys the own data key of the record under key, once the
// record is gone: under the key's stripe alone, so that a write sealing
// under it meanwhile either stands before — and keeps it — or makes a new
// one after.
func (e *sealedEngine) dropOwnKey(ctx context.Context, key string) error {
	keys, err := e.a.referenceKeys(ctx)
	if err != nil {
		return err
	}
	ref := keys.ownRef(e.s.id, key)
	release, err := e.z.alone(ctx, ref)
	if err != nil {
		return err
	}
	defer release()
	if _, err := e.inner.Get(ctx, key); !errors.Is(err, docstore.DocumentNotFound) {
		return nil // written again meanwhile: the key seals it
	}
	if e.inTrash(ctx, key) {
		return nil // trashed meanwhile: the key seals what a restoration brings back
	}
	_, err = e.z.destroy(ctx, ref)
	return err
}

// inTrash reports whether the record under key is in the trash: its
// versions read while no record is stored.
func (e *sealedEngine) inTrash(ctx context.Context, key string) bool {
	if e.s.revisions == 0 {
		return false
	}
	vk, ok := e.inner.(plug.VersionKeeper)
	if !ok {
		return false
	}
	_, err := vk.StoredVersions(ctx, key)
	return err == nil
}

// Watch registers the store's hooks with the engine inside.
func (e *sealedEngine) Watch(onWrite, onDelete func(key string)) { e.inner.Watch(onWrite, onDelete) }

// Close closes the engine inside.
func (e *sealedEngine) Close() error { return e.inner.Close() }

// Fold folds the engine's files, when it keeps some beside its data.
func (e *sealedEngine) Fold(ctx context.Context) error {
	if f, ok := e.inner.(plug.Folder); ok {
		return f.Fold(ctx)
	}
	return nil
}

// What kit asks of a sealing store beside the port ------------------------------

// refsAt are the data keys the record under key names in its boxes, as it
// rests: those an erasure of it destroys.
func (e *sealedEngine) refsAt(ctx context.Context, key string) ([]string, error) {
	d, err := e.inner.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	_, _, err = boxWalk(d.JSON, nil, "", false, func(_ memberAt, value []byte) ([]byte, bool, error) {
		if ref, ok := boxRef(value); ok {
			seen[ref] = true
		}
		return value, false, nil
	})
	return slices.Sorted(maps.Keys(seen)), err
}

// inClear reports whether the record under key, as it rests, holds a member
// kit seals in clear — written before its field was classified, or before
// kit sealed —, which its next write seals. A zero value holds nothing: an
// erased record's cleared members are left as they are.
func (e *sealedEngine) inClear(ctx context.Context, key string) (bool, error) {
	d, err := e.inner.Get(ctx, key)
	if err != nil {
		return false, err
	}
	plain, found := e.clearNow(), false
	_, _, err = sealWalk(d.JSON, e.s.plan().rules, "", false, func(at memberAt, value []byte) ([]byte, bool, error) {
		kept := !at.inList && plain[at.pointer]
		found = found || (!isBoxValue(value) && !kept && !zeroJSON(value, at.f.typ))
		return value, false, nil
	})
	return found, err
}

// sealing is the engine under a store's history and password policies: the
// store's sealing engine, or nil when it seals nothing.
func (s *storeCore) sealing() *sealedEngine {
	switch eng := s.engine().(type) {
	case *sealedEngine:
		return eng
	case *historied:
		se, _ := eng.StoreEngine.(*sealedEngine)
		return se
	}
	return nil
}
