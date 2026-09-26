// Package docstore — opening a store: the configuration checked, the indexes
// declared, and a persistent store loaded.
package docstore

import "encoding/json"

// Open builds a store from cfg, with the secondary indexes given: in memory
// without a filesystem, otherwise loaded from it — the snapshot, the overlay
// replayed on top, the indexes rebuilt, and the overlay folded when it holds
// anything. It creates the directories the store lives in, 0700, and writes
// its files 0600.
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
	s := &Store[T]{
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
		s.indexes = append(s.indexes, ix)
		s.byName[spec.Name] = ix
	}
	//: a persistent store reads what an earlier one left.
	if s.fs != nil {
		//: LoadFailed, PersistFailed or WriteUnconfirmed.
		if loadErr := s.load(); loadErr != nil {
			//: no store over files it could not read.
			return nil, loadErr
		}
	}
	//: the indexes, from whatever was loaded.
	if rebuildErr := s.rebuild(); rebuildErr != nil {
		//: IndexBroken or DocumentUndecodable.
		return nil, rebuildErr
	}
	//: ready.
	return s, nil
}
