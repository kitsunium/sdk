package docstore

import (
	"cmp"
	"slices"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// Trash moves the document stored under key to the trash, in one durable
// write under the writers' lock: it leaves every read and its index keys
// are freed, while its versions are kept beside the trashed document and
// the instant it was trashed. It calls OnDelete. DocumentNotFound when no
// document is stored under key, VersionsNotKept when the store keeps no
// versions: only a store that keeps versions has a trash.
func (s *Store[T]) Trash(key string) error {
	applied, err := s.trash(key)
	//: a trash that took effect is announced, WriteUnconfirmed included.
	if applied {
		s.onDelete.call(key)
	}
	//: nil, WriteUnconfirmed, or why nothing changed.
	return err
}

// Restore brings the document trashed under key back exactly as it was —
// the same JSON, the same current version — files it again and calls
// OnWrite. DocumentNotTrashed when no document is trashed under key,
// UniqueKeyTaken when another document took one of its unique keys
// meanwhile, DocumentUndecodable when it no longer decodes into the store's
// type; nothing changes on a refusal.
func (s *Store[T]) Restore(key string) error {
	applied, err := s.restore(key)
	//: a restoration that took effect is announced, WriteUnconfirmed included.
	if applied {
		s.onWrite.call(key)
	}
	//: nil, WriteUnconfirmed, or why nothing changed.
	return err
}

// Purge empties the trash of the document trashed under key: the trashed
// document and every version it kept go, in one durable write, and a
// document written later under the key starts again at version 1. It calls
// no hook and asks Held nothing, as Delete does not. DocumentNotTrashed
// when no document is trashed under key.
func (s *Store[T]) Purge(key string) error {
	//: a store that keeps none has no trash.
	if s.keep == 0 {
		//: VersionsNotKept, naming the store.
		return versionsNotKept(s.path)
	}
	s.writing.Lock()
	defer s.writing.Unlock()
	//: a closed store takes nothing.
	if s.closed {
		//: StoreClosed.
		return kerrs.Wrap(coredocstore.StoreClosed, kerrs.WrapParams{})
	}
	//: nothing in the trash under the key.
	if _, trashed := s.trashedRecord(key); !trashed {
		//: DocumentNotTrashed, naming no key.
		return notTrashed(s.path)
	}
	persistErr := s.persist(key, nil, true, nil)
	//: the filesystem refused: nothing changed anywhere.
	if persistErr != nil && !kerrs.HasCode(persistErr, coredocstore.CodeWriteUnconfirmed) {
		//: PersistFailed.
		return persistErr
	}
	s.mu.Lock()
	s.setVersions(key, nil)
	s.markPending(key)
	s.mu.Unlock()
	s.maybeFold()
	//: purged, and nil or WriteUnconfirmed.
	return persistErr
}

// Tombstones returns every document in the trash, in key order: its key and
// the instant it was trashed. It reads every document's versions, as List
// reads every document.
func (s *Store[T]) Tombstones() ([]coredocstore.TombstoneValue, error) {
	//: a store that keeps none has no trash.
	if s.keep == 0 {
		//: VersionsNotKept, naming the store.
		return nil, versionsNotKept(s.path)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	//: a closed store answers nothing.
	if s.closed {
		//: StoreClosed.
		return nil, kerrs.Wrap(coredocstore.StoreClosed, kerrs.WrapParams{})
	}
	trashed := 0
	//: counted first, so the answer is allocated once.
	for _, record := range s.versions {
		//: a document the store holds is not in the trash.
		if record.Trashed != nil {
			trashed++
		}
	}
	out := make([]coredocstore.TombstoneValue, 0, trashed)
	//: every record in the trash.
	for key, record := range s.versions {
		//: a document the store holds is not in the trash.
		if record.Trashed != nil {
			out = append(out, coredocstore.TombstoneValue{Key: key, At: record.Trashed.At})
		}
	}
	slices.SortFunc(out, func(a, b coredocstore.TombstoneValue) int {
		//: by key, as List orders documents: by their bytes.
		return cmp.Compare(a.Key, b.Key)
	})
	//: in key order.
	return out, nil
}

// trash runs Trash under the writers' lock, and reports whether it took
// effect.
func (s *Store[T]) trash(key string) (applied bool, err error) {
	//: a store that keeps none has no trash.
	if s.keep == 0 {
		//: VersionsNotKept, naming the store.
		return false, versionsNotKept(s.path)
	}
	s.writing.Lock()
	defer s.writing.Unlock()
	//: a closed store takes nothing.
	if s.closed {
		//: StoreClosed.
		return false, kerrs.Wrap(coredocstore.StoreClosed, kerrs.WrapParams{})
	}
	raw, found := s.docs[key]
	//: nothing to trash.
	if !found {
		//: DocumentNotFound, naming no key.
		return false, kerrs.Wrap(coredocstore.DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.path))
	}
	head, former := firstHead, []formerVersion(nil)
	//: a document versioned since its store kept versions; otherwise it is
	//: version 1, made at an instant nobody recorded.
	if record := s.versions[key]; record != nil {
		head, former = record.Current, record.Former
	}
	next := &versionsRecord{
		Trashed: &trashedDocument{At: s.now(), Document: compactJSON(raw)},
		Former:  former,
		Current: head,
	}
	persistErr := s.persist(key, nil, true, next)
	//: the filesystem refused: nothing changed anywhere.
	if persistErr != nil && !kerrs.HasCode(persistErr, coredocstore.CodeWriteUnconfirmed) {
		//: PersistFailed.
		return false, persistErr
	}
	s.mu.Lock()
	delete(s.docs, key)
	s.setVersions(key, next)
	s.unfile(key)
	s.markPending(key)
	s.mu.Unlock()
	s.maybeFold()
	//: trashed, and nil or WriteUnconfirmed.
	return true, persistErr
}

// restore runs Restore under the writers' lock, and reports whether it took
// effect.
func (s *Store[T]) restore(key string) (applied bool, err error) {
	//: a store that keeps none has no trash.
	if s.keep == 0 {
		//: VersionsNotKept, naming the store.
		return false, versionsNotKept(s.path)
	}
	s.writing.Lock()
	defer s.writing.Unlock()
	//: a closed store takes nothing.
	if s.closed {
		//: StoreClosed.
		return false, kerrs.Wrap(coredocstore.StoreClosed, kerrs.WrapParams{})
	}
	record, trashed := s.trashedRecord(key)
	//: nothing in the trash under the key.
	if !trashed {
		//: DocumentNotTrashed, naming no key.
		return false, notTrashed(s.path)
	}
	raw := record.Trashed.Document
	v, decodeErr := s.decode(raw)
	//: a trashed document the type no longer fits.
	if decodeErr != nil {
		//: DocumentUndecodable.
		return false, decodeErr
	}
	keys := s.indexKeys(v)
	//: a unique index filing one of its keys elsewhere since it was trashed.
	if name, taken := s.uniqueTaken(key, keys); taken {
		//: UniqueKeyTaken, naming the index and never the key.
		return false, kerrs.Wrap(coredocstore.UniqueKeyTaken, kerrs.WrapParams{}, kerrs.String("store", s.path), kerrs.String("index", name))
	}
	next := &versionsRecord{Current: record.Current, Former: record.Former}
	persistErr := s.persist(key, raw, false, next)
	//: the filesystem refused: nothing changed anywhere.
	if persistErr != nil && !kerrs.HasCode(persistErr, coredocstore.CodeWriteUnconfirmed) {
		//: PersistFailed.
		return false, persistErr
	}
	s.mu.Lock()
	s.docs[key] = raw
	s.setVersions(key, next)
	s.file(key, keys)
	s.markPending(key)
	s.mu.Unlock()
	s.maybeFold()
	//: restored, and nil or WriteUnconfirmed.
	return true, persistErr
}

// trashedRecord returns the versions of the document trashed under key, and
// whether one is. The caller holds the writers' lock.
func (s *Store[T]) trashedRecord(key string) (*versionsRecord, bool) {
	record := s.versions[key]
	//: a record holding a trashed document.
	return record, record != nil && record.Trashed != nil
}

// notTrashed is DocumentNotTrashed naming store.
func notTrashed(store string) error {
	//: the store, never a key.
	return kerrs.Wrap(coredocstore.DocumentNotTrashed, kerrs.WrapParams{}, kerrs.String("store", store))
}
