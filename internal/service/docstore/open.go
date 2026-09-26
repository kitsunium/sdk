// Package docstore — opening a store: the configuration checked, the indexes
// declared, and a persistent store loaded.
package docstore

import "encoding/json"

// Open builds a store from cfg, with the secondary indexes given: in memory
// without a filesystem, otherwise loaded from it — the snapshot, the overlay
// replayed on top, the indexes rebuilt, and the overlay folded when it holds
// anything. It creates the directories the store lives in, 0700, and writes
// its files 0600. An open it refuses writes no data: the fold comes after
// every check, so the files an operator must repair are left as they were.
//
//	accounts, err := docstore.Open(
//	    docstore.Config[Account]{Key: Account.ID, FS: data, Path: "members/accounts.json"},
//	    docstore.Unique("email", func(a Account) string { return a.Email }),
//	    docstore.Index("team", func(a Account) []string { return a.Teams }),
//	)
func Open[T any](cfg Config[T], indexes ...IndexSpec[T]) (*Store[T], error) {
	//: everything decidable without a filesystem is decided first.
	if invalid := cfg.validate(indexes); invalid != nil {
		//: StoreMisconfigured.
		return nil, invalid
	}
	store := newStore(cfg, indexes)
	//: LoadFailed, IndexBroken, DocumentUndecodable, PersistFailed or
	//: WriteUnconfirmed.
	if openErr := store.open(); openErr != nil {
		//: no store over files it could not trust or bring to rest.
		return nil, openErr
	}
	//: ready.
	return store, nil
}

// newStore builds the empty store a validated cfg and its indexes describe.
func newStore[T any](cfg Config[T], indexes []IndexSpec[T]) *Store[T] {
	store := &Store[T]{
		key:     cfg.Key,
		fs:      cfg.FS,
		byName:  make(map[string]*index[T], len(indexes)),
		docs:    map[string]json.RawMessage{},
		pending: map[string]struct{}{},
		path:    cfg.Path,
		overlay: cfg.Path + overlaySuffix,
		foldAt:  cfg.FoldAt,
	}
	//: the indexes, in declaration order.
	for _, spec := range indexes {
		ix := newIndex(spec)
		store.indexes = append(store.indexes, ix)
		store.byName[spec.Name] = ix
	}
	//: empty, and not loaded yet.
	return store
}

// open loads a persistent store, rebuilds the indexes, and writes the resting
// state only once every check passed: an open it refuses writes no data. Open
// holds the only reference, so no lock is taken.
func (s *Store[T]) open() error {
	needsFold := false
	//: a persistent store reads what an earlier one left.
	if s.fs != nil {
		var loadErr error
		needsFold, loadErr = s.load()
		//: LoadFailed.
		if loadErr != nil {
			//: no store over files it could not read.
			return loadErr
		}
	}
	//: the indexes, from whatever was loaded.
	if rebuildErr := s.rebuild(); rebuildErr != nil {
		//: IndexBroken or DocumentUndecodable, and nothing written.
		return rebuildErr
	}
	//: already at rest, or in memory.
	if !needsFold {
		//: nothing to write.
		return nil
	}
	//: the resting state. PersistFailed or WriteUnconfirmed leaves files that
	//: still hold every document, and the next Open tries again.
	return s.fold()
}
