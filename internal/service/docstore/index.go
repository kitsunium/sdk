// Package docstore — the secondary indexes, kept beside the documents under
// the store's own locks.
//
// An index is rebuilt from the documents when the store opens, and every
// write — Put, Insert, Replace, Update, Delete — maintains it in the same
// critical section that changes the document. A reader therefore never sees a
// document and an index that disagree, and a write a unique index refuses
// leaves both exactly as they were.
package docstore

import (
	"fmt"
	"maps"
	"slices"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// index is one secondary index of a Store[T]. Its maps are guarded by the
// store: written under both of its locks, read under either.
type index[T any] struct {
	// keys is the declared key function.
	keys func(T) []string
	// entries maps an index key to the store keys of the documents under it;
	// a unique index holds at most one per key.
	entries map[string]map[string]struct{}
	// owned maps a store key to its document's index keys: what a rewrite or
	// a deletion takes out.
	owned map[string][]string
	// name reads the index.
	name string
	// unique refuses a second holder of a key.
	unique bool
}

// newIndex builds the empty index a spec declares.
func newIndex[T any](spec IndexSpec[T]) *index[T] {
	//: empty until the store loads its documents.
	return &index[T]{
		keys:    spec.Keys,
		entries: map[string]map[string]struct{}{},
		owned:   map[string][]string{},
		name:    spec.Name,
		unique:  spec.Unique,
	}
}

// keysOf returns v's keys in the index: the declared function's answer
// without its empty or repeated keys.
func (ix *index[T]) keysOf(v T) []string {
	raw := ix.keys(v)
	out := make([]string, 0, len(raw))
	//: in the order the function gave them.
	for _, k := range raw {
		//: an empty key is not a key, and a repeated one is filed once.
		if k != "" && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	//: the keys to file.
	return out
}

// taken reports whether one of keys is already held, in this unique index, by
// a document other than the one stored under owner.
func (ix *index[T]) taken(owner string, keys []string) bool {
	//: only a unique index refuses anything.
	if !ix.unique {
		//: any number of holders.
		return false
	}
	//: each key of the document being written.
	for _, k := range keys {
		//: a holder other than the document itself.
		for other := range ix.entries[k] {
			//: rewriting a document with its own key is not a conflict.
			if other != owner {
				//: held by somebody else.
				return true
			}
		}
	}
	//: free, or the document's own.
	return false
}

// set files the document stored under owner under keys, replacing whatever
// it was filed under before.
func (ix *index[T]) set(owner string, keys []string) {
	ix.drop(owner)
	//: one entry per key.
	for _, k := range keys {
		holders := ix.entries[k]
		//: the first holder of this key.
		if holders == nil {
			holders = map[string]struct{}{}
			ix.entries[k] = holders
		}
		holders[owner] = struct{}{}
	}
	//: remembered for the next rewrite or deletion.
	if len(keys) > 0 {
		ix.owned[owner] = keys
	}
}

// drop takes the document stored under owner out of the index.
func (ix *index[T]) drop(owner string) {
	//: every key it was filed under.
	for _, k := range ix.owned[owner] {
		//: the key may be shared with other documents.
		if holders := ix.entries[k]; holders != nil {
			delete(holders, owner)
			//: a key nobody holds any more is forgotten.
			if len(holders) == 0 {
				delete(ix.entries, k)
			}
		}
	}
	delete(ix.owned, owner)
}

// holders returns the store keys filed under key, in store-key order.
func (ix *index[T]) holders(key string) []string {
	//: ordered, so Find answers identically twice.
	return slices.Sorted(maps.Keys(ix.entries[key]))
}

// indexKeys computes v's keys in every index, in declaration order. It runs
// the caller's key functions, so a write computes it before taking a lock
// whenever the document is already known.
func (s *Store[T]) indexKeys(v T) [][]string {
	//: a store without indexes computes nothing.
	if len(s.indexes) == 0 {
		//: no keys.
		return nil
	}
	out := make([][]string, len(s.indexes))
	//: one key list per index.
	for i, ix := range s.indexes {
		out[i] = ix.keysOf(v)
	}
	//: aligned with s.indexes.
	return out
}

// uniqueTaken reports the first unique index that refuses the keys of the
// document stored under owner. The caller holds the writers' lock.
func (s *Store[T]) uniqueTaken(owner string, keys [][]string) (name string, taken bool) {
	//: in declaration order, so the refusal names the first index that fails.
	for i, ix := range s.indexes {
		//: held by another document.
		if ix.taken(owner, keys[i]) {
			//: the index, never the key.
			return ix.name, true
		}
	}
	//: every unique index accepts it.
	return "", false
}

// file files the document stored under owner in every index. The caller holds
// both locks and has checked uniqueTaken.
func (s *Store[T]) file(owner string, keys [][]string) {
	//: every index, with its own keys.
	for i, ix := range s.indexes {
		ix.set(owner, keys[i])
	}
}

// unfile takes the document stored under owner out of every index. The caller
// holds both locks.
func (s *Store[T]) unfile(owner string) {
	//: every index.
	for _, ix := range s.indexes {
		ix.drop(owner)
	}
}

// rebuild files every document in every index, when the store opens. A
// document that no longer decodes, two documents sharing a key in a unique
// index, or a key function that panics refuses the open: a unique index that
// does not hold would be a lie every Lookup tells.
func (s *Store[T]) rebuild() (err error) {
	//: a store without indexes decodes nothing on open.
	if len(s.indexes) == 0 {
		//: nothing to rebuild.
		return nil
	}
	defer func() {
		recovered := recover()
		//: the ordinary path.
		if recovered == nil {
			return
		}
		//: a key function that panics on stored data is data the index cannot
		//: hold; the value travels as a field, never as the origin.
		err = kerrs.Wrap(IndexBroken, kerrs.WrapParams{},
			kerrs.String("problem", "a key function panicked"), kerrs.String("panic", fmt.Sprint(recovered)))
	}()
	//: in key order, so the conflict reported is the same on every open.
	for _, key := range sortedKeys(s.docs) {
		v, decodeErr := s.decode(s.docs[key])
		//: a document the type no longer fits cannot be indexed.
		if decodeErr != nil {
			//: DocumentUndecodable.
			return decodeErr
		}
		keys := s.indexKeys(v)
		//: two documents under one unique key.
		if name, taken := s.uniqueTaken(key, keys); taken {
			//: IndexBroken, naming the index and never the key.
			return kerrs.Wrap(IndexBroken, kerrs.WrapParams{},
				kerrs.String("problem", "two documents share a unique key"), kerrs.String("index", name))
		}
		s.file(key, keys)
	}
	//: every index holds.
	return nil
}
