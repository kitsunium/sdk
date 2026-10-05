package kit

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"

	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// A store that seals, as kit's port sees it (ADR 0004, ADR 0006 §4): every
// call of storeEngine[T], its records opened where they are read and sealed
// where they are written (seal_store.go), and what kit's erasure and the
// privacy command ask of it beside the port.

// Get reads the record key, opened.
func (e *sealedEngine[T]) Get(ctx context.Context, key string) (T, error) {
	d, err := e.inner.Get(ctx, key)
	if err != nil {
		var zero T
		return zero, err
	}
	return e.openRecord(ctx, key, d.raw)
}

// List reads every record, opened.
func (e *sealedEngine[T]) List(ctx context.Context) ([]T, error) {
	entries, err := e.resting(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(entries))
	for _, en := range entries {
		v, err := e.openRecord(ctx, en.Key, en.JSON)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
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
func (e *sealedEngine[T]) resting(ctx context.Context, limit int) ([]docstore.Entry, error) {
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
		out[i] = docstore.Entry{Key: e.docKey(d), JSON: d.raw}
	}
	return out, nil
}

// Filter reads the records keep keeps, opened.
func (e *sealedEngine[T]) Filter(ctx context.Context, keep func(T) bool) ([]T, error) {
	all, err := e.List(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, v := range all {
		if keep(v) {
			out = append(out, v)
		}
	}
	return out, nil
}

// Lookup reads the record whose unique index holds key, opened.
func (e *sealedEngine[T]) Lookup(ctx context.Context, index, key string) (T, error) {
	d, err := e.inner.Lookup(ctx, index, key)
	if err != nil {
		var zero T
		return zero, err
	}
	return e.openFound(ctx, d)
}

// Find reads the records an index files under key, opened.
func (e *sealedEngine[T]) Find(ctx context.Context, index, key string) ([]T, error) {
	found, err := e.inner.Find(ctx, index, key)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(found))
	for _, d := range found {
		v, err := e.openFound(ctx, d)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// openFound opens a record an index found, its key read first.
func (e *sealedEngine[T]) openFound(ctx context.Context, d sealedDoc) (T, error) {
	return e.openRecord(ctx, e.docKey(d), d.raw)
}

// Count is how many records the store holds.
func (e *sealedEngine[T]) Count(ctx context.Context) (int, error) { return e.inner.Count(ctx) }

// Entries are the records as they rest, sealed members as boxes: the Studio
// shows them sealed (classify_walk.go).
func (e *sealedEngine[T]) Entries(ctx context.Context, limit int) ([]json.RawMessage, error) {
	return e.inner.Entries(ctx, limit)
}

// Write stores v as mode says. A record that may replace another is sealed
// under the store's writers' lock — through the engine's Update —, so that
// the data key it chooses is chosen after every write before it: a hold
// placed meanwhile, and the move of the record under its own key, are never
// undone by a write sealed before them. A Put whose key is new inserts, and
// replaces when another writer inserted it meanwhile.
func (e *sealedEngine[T]) Write(ctx context.Context, v T, mode writeMode) error {
	key := e.s.keyOf(v)
	if mode == insertOnly || key == "" || !e.s.plan().sealsAny() {
		return e.insert(ctx, v, mode)
	}
	var err error
	for range writeAttempts {
		err = e.replace(ctx, key, v)
		if mode == replaceOnly || !errors.Is(err, docstore.DocumentNotFound) {
			return err
		}
		if err = e.insert(ctx, v, insertOnly); !errors.Is(err, docstore.DocumentExists) {
			return err
		}
	}
	return err
}

// insert seals v and stores it as mode says, the engine checking what it
// finds under the key.
func (e *sealedEngine[T]) insert(ctx context.Context, v T, mode writeMode) error {
	d, release, err := e.sealRecord(ctx, v)
	if err != nil {
		return err
	}
	defer release()
	return e.inner.Write(ctx, d, mode)
}

// replace stores v over the record under key, sealed under the store's
// writers' lock; the record it replaces is not opened — but on a store that
// keeps versions, which opens it to tell a change from none
// (revisions_engine.go).
func (e *sealedEngine[T]) replace(ctx context.Context, key string, v T) error {
	if e.s.revisions > 0 && !inPlace(ctx) {
		_, err := e.keptUpdate(ctx, key, func(cur *T) error { *cur = v; return nil }, true)
		return err
	}
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()
	_, err := e.inner.Update(ctx, key, func(d *sealedDoc) error {
		next, rel, err := e.sealRecord(ctx, v)
		if err != nil {
			return err
		}
		release = rel
		*d = next
		return nil
	})
	return err
}

// Update applies fn to the record under key, opened, and stores it sealed
// under the store's writers' lock. On a store that keeps versions, a record
// that means what it meant is kept as it rests, or sealed again in place
// (revisions_engine.go).
func (e *sealedEngine[T]) Update(ctx context.Context, key string, fn func(*T) error) (T, error) {
	if e.s.revisions > 0 && !inPlace(ctx) {
		return e.keptUpdate(ctx, key, fn, false)
	}
	return e.update(ctx, key, fn)
}

// update applies fn to the record under key, opened, and stores it sealed
// anew.
func (e *sealedEngine[T]) update(ctx context.Context, key string, fn func(*T) error) (T, error) {
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()
	d, err := e.inner.Update(ctx, key, func(d *sealedDoc) error {
		v, err := e.openRecord(ctx, key, d.raw)
		if err != nil {
			return err
		}
		if err := fn(&v); err != nil {
			return err
		}
		next, rel, err := e.sealRecord(ctx, v)
		if err != nil {
			return err
		}
		release = rel
		*d = next
		return nil
	})
	if err != nil {
		var zero T
		return zero, err
	}
	return openedAs[T](d.open), nil
}

// Delete removes the record under key, then its own data key, when it had
// one: every copy of what that key sealed stops opening. Inside a
// transaction ([Transact]) the key goes once it commits: a rollback puts the
// record back, sealed under it.
func (e *sealedEngine[T]) Delete(ctx context.Context, key string) error {
	err := e.inner.Delete(ctx, key)
	if (err != nil && !errors.Is(err, docstore.WriteUnconfirmed)) || !e.s.plan().sealsAny() {
		return err
	}
	drop := func(ctx context.Context) {
		if derr := e.dropOwnKey(ctx, key); derr != nil {
			logger.Warn(ctx, e.a.log, "a deleted record's own data key could not be destroyed",
				logger.String("store", e.s.id), logger.String("error", describeText(derr)))
		}
	}
	if !hold(ctx, heldEffect{node: e.s.id, inside: true, release: func() error {
		drop(withoutUnit(context.WithoutCancel(ctx)))
		return nil
	}}) {
		drop(ctx)
	}
	return err
}

// dropOwnKey destroys the own data key of the record under key, once the
// record is gone: under the key's stripe alone, so that a write sealing
// under it meanwhile either stands before — and keeps it — or makes a new
// one after.
func (e *sealedEngine[T]) dropOwnKey(ctx context.Context, key string) error {
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
	_, err = e.z.destroy(ctx, ref)
	return err
}

// Watch registers the store's hooks with the engine inside.
func (e *sealedEngine[T]) Watch(onWrite, onDelete func(key string)) { e.inner.Watch(onWrite, onDelete) }

// Close closes the engine inside.
func (e *sealedEngine[T]) Close() error { return e.inner.Close() }

// Fold folds the engine's files, when it keeps some beside its data.
func (e *sealedEngine[T]) Fold(ctx context.Context) error {
	if f, ok := e.inner.(folder); ok {
		return f.Fold(ctx)
	}
	return nil
}

// What kit asks of a sealing store beside the port ------------------------------

// refsAt are the data keys the record under key names in its boxes, as it
// rests: those an erasure of it destroys.
func (e *sealedEngine[T]) refsAt(ctx context.Context, key string) ([]string, error) {
	d, err := e.inner.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	_, _, err = boxWalk(d.raw, nil, "", false, func(_ memberAt, value []byte) ([]byte, bool, error) {
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
func (e *sealedEngine[T]) inClear(ctx context.Context, key string) (bool, error) {
	d, err := e.inner.Get(ctx, key)
	if err != nil {
		return false, err
	}
	plain, found := e.clearNow(), false
	_, _, err = sealWalk(d.raw, e.s.plan().rules, "", false, func(at memberAt, value []byte) ([]byte, bool, error) {
		kept := !at.inList && plain[at.pointer]
		found = found || (!isBoxValue(value) && !kept && !zeroJSON(value, at.f.typ))
		return value, false, nil
	})
	return found, err
}

// sealing is the engine under a store's history and password policies: the
// store's sealing engine, or nil when it seals nothing.
func (s *StoreService[T]) sealing() *sealedEngine[T] {
	switch eng := s.engine().(type) {
	case *sealedEngine[T]:
		return eng
	case *historied[T]:
		se, _ := eng.storeEngine.(*sealedEngine[T])
		return se
	}
	return nil
}
