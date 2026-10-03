// Package docstore — the writes: prepared outside every lock, checked and
// persisted under the writers' lock, applied under both.
package docstore

import (
	"encoding/json"
	"errors"
	"fmt"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// writeMode says what a write expects to find under its key.
type writeMode uint8

// The three write modes.
const (
	// upsert creates or replaces.
	upsert writeMode = iota
	// insertOnly refuses an existing key.
	insertOnly
	// replaceOnly refuses a missing key, so a document deleted meanwhile is
	// never brought back.
	replaceOnly
)

// prepared is a document ready to be stored: its JSON, its keys in every
// index, and its own key.
type prepared struct {
	// raw is the document's JSON.
	raw json.RawMessage
	// keys are its keys in every index, aligned with the store's indexes.
	keys [][]string
	// key is its store key.
	key string
}

// Put stores v under its key, creating it or replacing whatever was there.
// On a store that keeps versions, it makes one, stamped with nothing.
func (s *Store[T]) Put(v T) error {
	//: anything under the key is fine.
	return s.store(v, upsert, coredocstore.StampValue{})
}

// Insert stores v, which must be new: DocumentExists when its key is taken.
func (s *Store[T]) Insert(v T) error {
	//: nothing may be under the key.
	return s.store(v, insertOnly, coredocstore.StampValue{})
}

// Replace stores v over the document already under its key: DocumentNotFound
// when there is none, so a document deleted meanwhile is never brought back.
func (s *Store[T]) Replace(v T) error {
	//: a document must be under the key.
	return s.store(v, replaceOnly, coredocstore.StampValue{})
}

// Update applies fn to a copy of the document stored under key and stores the
// result, atomically with respect to every other write. An error from fn is
// returned as it is and changes nothing, and so does a result whose key is not
// key (DocumentKeyChanged). It returns the stored result.
//
// fn runs under the writers' lock: it may read this store, and must not write
// to it — the write would wait for the lock fn holds, forever.
func (s *Store[T]) Update(key string, fn func(*T) error) (T, error) {
	//: a new version, stamped with nothing, where versions are kept.
	return s.UpdateStamped(key, coredocstore.StampValue{}, fn)
}

// Delete removes the document stored under key, or returns DocumentNotFound.
// Its versions go with it.
func (s *Store[T]) Delete(key string) error {
	applied, err := s.remove(key)
	//: a deletion that took effect is announced, WriteUnconfirmed included.
	if applied {
		s.onDelete.call(key)
	}
	//: nil, WriteUnconfirmed, or why nothing changed.
	return err
}

// store prepares v outside every lock, commits it, and announces it.
func (s *Store[T]) store(v T, mode writeMode, stamp coredocstore.StampValue) error {
	p, prepErr := s.prepare(v)
	//: DocumentKeyEmpty or DocumentUnencodable: nothing was locked.
	if prepErr != nil {
		//: nothing changed.
		return prepErr
	}
	applied, err := s.commit(p, mode, stamp)
	//: a write that took effect is announced, WriteUnconfirmed included.
	if applied {
		s.onWrite.call(p.key)
	}
	//: nil, WriteUnconfirmed, or why nothing changed.
	return err
}

// prepare computes everything a write needs from v: its key, its JSON and its
// index keys. It runs the caller's functions, so a write runs it before it
// takes a lock whenever the document is already known.
func (s *Store[T]) prepare(v T) (prepared, error) {
	key, raw, err := encodeAs(s.path, s.key, v)
	//: DocumentKeyEmpty or DocumentUnencodable.
	if err != nil {
		//: nothing to store.
		return prepared{}, err
	}
	//: ready to be checked and stored.
	return prepared{raw: raw, keys: s.indexKeys(v), key: key}, nil
}

// encodeAs computes v's key and JSON, or refuses v naming store and never a
// byte of v. Every engine encodes through it, so a document one engine refuses
// is refused by the other.
func encodeAs[T any](store string, keyOf func(T) string, v T) (key string, raw json.RawMessage, err error) {
	key = keyOf(v)
	//: a document the store could never find again.
	if key == "" {
		//: DocumentKeyEmpty.
		return "", nil, kerrs.Wrap(coredocstore.DocumentKeyEmpty, kerrs.WrapParams{}, kerrs.String("store", store))
	}
	raw, err = json.Marshal(v)
	//: a channel, a function, a cycle, a failing MarshalJSON.
	if err != nil {
		//: DocumentUnencodable; what failed, never what it held.
		return "", nil, kerrs.Wrap(coredocstore.DocumentUnencodable, kerrs.WrapParams{},
			kerrs.String("store", store), kerrs.String("cause", encodeCause(err)))
	}
	//: the key and the document as written.
	return key, raw, nil
}

// commit checks a prepared write against the store and stores it. It reports
// whether the write took effect.
func (s *Store[T]) commit(p prepared, mode writeMode, stamp coredocstore.StampValue) (applied bool, err error) {
	s.writing.Lock()
	defer s.writing.Unlock()
	//: a closed store takes nothing.
	if s.closed {
		//: StoreClosed.
		return false, kerrs.Wrap(coredocstore.StoreClosed, kerrs.WrapParams{})
	}
	_, existed := s.docs[p.key]
	//: an insertion over an existing document.
	if mode == insertOnly && existed {
		//: DocumentExists, naming no key.
		return false, kerrs.Wrap(coredocstore.DocumentExists, kerrs.WrapParams{}, kerrs.String("store", s.path))
	}
	//: a replacement of a document that is gone.
	if mode == replaceOnly && !existed {
		//: DocumentNotFound, naming no key.
		return false, kerrs.Wrap(coredocstore.DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.path))
	}
	//: durable, then applied, then perhaps folded.
	return s.persistAndApply(p, stamp)
}

// modify runs Update's read-modify-write under the writers' lock.
func (s *Store[T]) modify(key string, stamp coredocstore.StampValue, fn func(*T) error) (result T, applied bool, err error) {
	var zero T
	s.writing.Lock()
	defer s.writing.Unlock()
	//: a closed store takes nothing.
	if s.closed {
		//: StoreClosed.
		return zero, false, kerrs.Wrap(coredocstore.StoreClosed, kerrs.WrapParams{})
	}
	raw, found := s.docs[key]
	//: nothing to update.
	if !found {
		//: DocumentNotFound, naming no key.
		return zero, false, kerrs.Wrap(coredocstore.DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.path))
	}
	v, decodeErr := s.decode(raw)
	//: a stored document the type no longer fits.
	if decodeErr != nil {
		//: DocumentUndecodable.
		return zero, false, decodeErr
	}
	//: the caller's change, whose error travels untouched.
	if fnErr := fn(&v); fnErr != nil {
		//: nothing changed.
		return zero, false, fnErr
	}
	p, prepErr := s.prepare(v)
	//: DocumentKeyEmpty or DocumentUnencodable.
	if prepErr != nil {
		//: nothing changed.
		return zero, false, prepErr
	}
	//: an update is not a rename.
	if p.key != key {
		//: DocumentKeyChanged.
		return zero, false, kerrs.Wrap(coredocstore.DocumentKeyChanged, kerrs.WrapParams{}, kerrs.String("store", s.path))
	}
	applied, err = s.persistAndApply(p, stamp)
	//: a write that did not take effect returns nothing.
	if !applied {
		//: the refusal.
		return zero, false, err
	}
	//: the stored result, with WriteUnconfirmed when that is its state.
	return v, true, err
}

// persistAndApply checks the unique indexes, makes the write durable — the
// document and its versions in one entry — applies it, and folds when the
// overlay has grown enough. The caller holds the writers' lock.
func (s *Store[T]) persistAndApply(p prepared, stamp coredocstore.StampValue) (applied bool, err error) {
	//: a unique index already filing one of the keys elsewhere.
	if name, taken := s.uniqueTaken(p.key, p.keys); taken {
		//: UniqueKeyTaken, naming the index and never the key.
		return false, kerrs.Wrap(coredocstore.UniqueKeyTaken, kerrs.WrapParams{}, kerrs.String("store", s.path), kerrs.String("index", name))
	}
	versions := s.nextVersions(p.key, p.raw, stamp)
	persistErr := s.persist(p.key, p.raw, false, versions)
	//: the filesystem refused: nothing changed anywhere.
	if persistErr != nil && !kerrs.HasCode(persistErr, coredocstore.CodeWriteUnconfirmed) {
		//: PersistFailed.
		return false, persistErr
	}
	s.mu.Lock()
	s.docs[p.key] = p.raw
	s.setVersions(p.key, versions)
	s.file(p.key, p.keys)
	s.markPending(p.key)
	s.mu.Unlock()
	s.maybeFold()
	//: applied, and nil or WriteUnconfirmed.
	return true, persistErr
}

// remove runs Delete under the writers' lock.
func (s *Store[T]) remove(key string) (applied bool, err error) {
	s.writing.Lock()
	defer s.writing.Unlock()
	//: a closed store removes nothing.
	if s.closed {
		//: StoreClosed.
		return false, kerrs.Wrap(coredocstore.StoreClosed, kerrs.WrapParams{})
	}
	//: nothing to remove.
	if _, found := s.docs[key]; !found {
		//: DocumentNotFound, naming no key.
		return false, kerrs.Wrap(coredocstore.DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.path))
	}
	persistErr := s.persist(key, nil, true, nil)
	//: the filesystem refused: nothing changed anywhere.
	if persistErr != nil && !kerrs.HasCode(persistErr, coredocstore.CodeWriteUnconfirmed) {
		//: PersistFailed.
		return false, persistErr
	}
	s.mu.Lock()
	delete(s.docs, key)
	s.setVersions(key, nil)
	s.unfile(key)
	s.markPending(key)
	s.mu.Unlock()
	s.maybeFold()
	//: removed, and nil or WriteUnconfirmed.
	return true, persistErr
}

// encodeCause describes an encoding failure without the value that failed:
// an unsupported value's text and a MarshalJSON's own error can both quote
// what the document held.
func encodeCause(err error) string {
	//: a type encoding/json cannot represent: the type is the program's.
	if unsupportedType, unsupported := errors.AsType[*json.UnsupportedTypeError](err); unsupported {
		//: the type.
		return "unsupported type " + unsupportedType.Type.String()
	}
	//: a MarshalJSON or MarshalText that failed: its type, not its words.
	if marshalerErr, failed := errors.AsType[*json.MarshalerError](err); failed {
		//: the type whose method failed.
		return "the marshaler of " + marshalerErr.Type.String() + " failed"
	}
	//: anything else, by kind.
	return fmt.Sprintf("%T", err)
}
