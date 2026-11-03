package docstore

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
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
	// sorted is an ordered index's entries in (key, owner) order, which Range
	// searches; nil for an index declared without an order. It holds what
	// owned holds, except while Open files the loaded documents (deferred).
	sorted []indexEntry
	// name reads the index.
	name string
	// unique refuses a second holder of a key.
	unique bool
	// order says whether sorted is kept: never, at every write, or not yet
	// while Open files the loaded documents.
	order orderMode
}

// orderMode is how an index keeps its sorted entries.
type orderMode uint8

const (
	// unordered keeps no sorted entries: an index declared without Ordered.
	unordered orderMode = iota
	// ordered keeps sorted in step with every write.
	ordered
	// deferred skips sorted while Open files every loaded document, which
	// resort then sorts once: one insertion per document would move the
	// whole list each time.
	deferred
)

// indexEntry is one entry of an ordered index: an index key and the store
// key of a document filed under it.
type indexEntry struct {
	// key is the index key.
	key string
	// owner is the store key of the document filed under it.
	owner string
}

// compareEntries orders an ordered index's entries: by index key, then by
// store key, both as bytes.
func compareEntries(a, b indexEntry) int {
	//: the index key first.
	if c := strings.Compare(a.key, b.key); c != 0 {
		//: different keys.
		return c
	}
	//: then the document.
	return strings.Compare(a.owner, b.owner)
}

// newIndex builds the empty index a spec declares.
func newIndex[T any](spec coredocstore.IndexSpec[T]) *index[T] {
	//: empty until the store loads its documents.
	return &index[T]{
		keys:    spec.Keys,
		entries: map[string]map[string]struct{}{},
		owned:   map[string][]string{},
		name:    spec.Name,
		unique:  spec.Unique,
		order:   orderOf(spec),
	}
}

// orderOf is the order mode an index declared by spec starts in.
func orderOf[T any](spec coredocstore.IndexSpec[T]) orderMode {
	//: an index declared Ordered.
	if spec.Ordered {
		//: kept at every write.
		return ordered
	}
	//: none.
	return unordered
}

// keysOf returns v's keys in the index: the declared function's answer
// without its empty or repeated keys.
func (ix *index[T]) keysOf(v T) []string {
	//: the rule both engines file by.
	return fileableKeys(ix.keys(v))
}

// fileableKeys returns the keys an index files out of what its function
// answered: in the order given, without the empty key — which is no key — and
// with a repeated key once. The memory, file and SQL engines all file by it,
// so a document has the same keys in every one of them.
func fileableKeys(raw []string) []string {
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
	//: an ordered index files each key in its place.
	if ix.order == ordered {
		//: one entry per key, by binary search.
		for _, k := range keys {
			entry := indexEntry{key: k, owner: owner}
			at, _ := slices.BinarySearchFunc(ix.sorted, entry, compareEntries)
			ix.sorted = slices.Insert(ix.sorted, at, entry)
		}
	}
}

// drop takes the document stored under owner out of the index.
func (ix *index[T]) drop(owner string) {
	//: every key it was filed under.
	for _, k := range ix.owned[owner] {
		//: an ordered index forgets the entry where it sits.
		if ix.order == ordered {
			//: found by binary search; always there, filed by set.
			if at, found := slices.BinarySearchFunc(ix.sorted, indexEntry{key: k, owner: owner}, compareEntries); found {
				ix.sorted = slices.Delete(ix.sorted, at, at+1)
			}
		}
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

// resort builds an ordered index's sorted entries from what it owns, once,
// and ends the deferral Open filed the loaded documents under.
func (ix *index[T]) resort() {
	//: an index without an order keeps no list.
	if ix.order == unordered {
		//: nothing to sort.
		return
	}
	ix.order = ordered
	n := 0
	//: how many entries.
	for _, keys := range ix.owned {
		n += len(keys)
	}
	ix.sorted = make([]indexEntry, 0, n)
	//: every document's keys.
	for owner, keys := range ix.owned {
		//: one entry per key.
		for _, k := range keys {
			ix.sorted = append(ix.sorted, indexEntry{key: k, owner: owner})
		}
	}
	slices.SortFunc(ix.sorted, compareEntries)
}

// between returns the entries of an ordered index under a key from lo,
// included, to hi, excluded — no upper bound when hi is empty —, at most
// limit of them when limit is positive. The caller holds a lock.
func (ix *index[T]) between(lo, hi string, limit int) []indexEntry {
	//: the first entry at or above lo; an empty owner sorts first.
	at, _ := slices.BinarySearchFunc(ix.sorted, indexEntry{key: lo}, compareEntries)
	end := len(ix.sorted)
	//: an upper bound ends the walk at the first key at or above it.
	if hi != "" {
		stop, _ := slices.BinarySearchFunc(ix.sorted[at:], indexEntry{key: hi}, compareEntries)
		end = at + stop
	}
	//: a positive limit caps the answer.
	if limit > 0 && end-at > limit {
		end = at + limit
	}
	//: the entries in range, in order; the caller copies what it hands out.
	return ix.sorted[at:end]
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

// rebuild checks every loaded document and files it in every index, when the
// store opens. It refuses the open for a document that no longer decodes
// (DocumentUndecodable), one stored under another key than the one its own
// Key gives (LoadFailed: the store would answer under a key its own Update
// refuses), two documents sharing a key in a unique index, or a key function
// that panics (IndexBroken: a unique index that does not hold would be a lie
// every Lookup tells).
func (s *Store[T]) rebuild() (err error) {
	s.deferOrders()
	defer func() {
		recovered := recover()
		//: the ordinary path.
		if recovered == nil {
			return
		}
		//: a key function that panics on stored data is data the index cannot
		//: hold; the value travels as a field, never as the origin.
		err = kerrs.Wrap(coredocstore.IndexBroken, kerrs.WrapParams{},
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
		//: a document filed under a key its own Key does not give — an edited
		//: file, or a key function that changed.
		if s.key(v) != key {
			//: LoadFailed, naming the file and never either key.
			return loadFailed(s.origin(key), "holds a document under another key than its own", nil)
		}
		keys := s.indexKeys(v)
		//: two documents under one unique key.
		if name, taken := s.uniqueTaken(key, keys); taken {
			//: IndexBroken, naming the index and never the key.
			return kerrs.Wrap(coredocstore.IndexBroken, kerrs.WrapParams{},
				kerrs.String("problem", "two documents share a unique key"), kerrs.String("index", name))
		}
		s.file(key, keys)
	}
	//: the ordered indexes sorted once, now that every document is filed.
	for _, ix := range s.indexes {
		ix.resort()
	}
	//: every index holds.
	return nil
}

// deferOrders makes every ordered index skip its sorted list while Open
// files the loaded documents, so that list is sorted once at the end rather
// than moved by one insertion per document.
func (s *Store[T]) deferOrders() {
	//: every index.
	for _, ix := range s.indexes {
		//: only an ordered index has a list to defer.
		if ix.order == ordered {
			ix.order = deferred
		}
	}
}
