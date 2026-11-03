package docstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// Revision returns the document stored under key and its revision — the
// SHA-256 of its JSON as json.Marshal writes it, so a fold, a reopen or
// the memory store leave it unchanged — read under the read lock.
// DocumentNotFound when no document is stored under key.
func (s *Store[T]) Revision(key string) (T, coredocstore.RevisionValue, error) {
	var zero T
	s.mu.RLock()
	raw, found := s.docs[key]
	closed := s.closed
	s.mu.RUnlock()
	//: a closed store answers nothing.
	if closed {
		//: StoreClosed.
		return zero, "", kerrs.Wrap(coredocstore.StoreClosed, kerrs.WrapParams{})
	}
	//: a miss — a trashed document included, which is not in docs.
	if !found {
		//: DocumentNotFound, naming no key.
		return zero, "", kerrs.Wrap(coredocstore.DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.path))
	}
	v, err := s.decode(raw)
	//: a stored document the type no longer fits.
	if err != nil {
		//: DocumentUndecodable.
		return zero, "", err
	}
	//: the copy, and the token naming the JSON it was decoded from.
	return v, revisionOf(raw), nil
}

// ReplaceIf stores v over the document under its key, as Replace does,
// when that document's revision is still rev — compared under the
// writers' lock, which the write then keeps. RevisionMismatch otherwise,
// DocumentNotFound when there is no document.
func (s *Store[T]) ReplaceIf(v T, rev coredocstore.RevisionValue) error {
	p, prepErr := s.prepare(v)
	//: DocumentKeyEmpty or DocumentUnencodable: nothing was locked.
	if prepErr != nil {
		//: nothing changed.
		return prepErr
	}
	applied, err := s.commitIf(p, rev)
	//: a write that took effect is announced, WriteUnconfirmed included.
	if applied {
		s.onWrite.call(p.key)
	}
	//: nil, WriteUnconfirmed, or why nothing changed.
	return err
}

// commitIf is commit for ReplaceIf: the document must be under the key, and
// its revision must be rev.
func (s *Store[T]) commitIf(p prepared, rev coredocstore.RevisionValue) (applied bool, err error) {
	s.writing.Lock()
	defer s.writing.Unlock()
	//: a closed store takes nothing.
	if s.closed {
		//: StoreClosed.
		return false, kerrs.Wrap(coredocstore.StoreClosed, kerrs.WrapParams{})
	}
	stored, found := s.docs[p.key]
	//: a replacement of a document that is gone is never a creation.
	if !found {
		//: DocumentNotFound, naming no key.
		return false, kerrs.Wrap(coredocstore.DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.path))
	}
	//: another write changed the document since rev was read.
	if revisionOf(stored) != rev {
		//: RevisionMismatch, naming neither the key nor the revision.
		return false, kerrs.Wrap(coredocstore.RevisionMismatch, kerrs.WrapParams{}, kerrs.String("store", s.path))
	}
	//: durable, then applied, as Replace.
	return s.persistAndApply(p, coredocstore.StampValue{})
}

// revisionOf is the file engine's revision of a stored document: the SHA-256,
// in lowercase hexadecimal, of its JSON as json.Marshal writes it — compact,
// HTML-escaped. A write stores exactly that; the snapshot a fold writes holds
// it indented, and an operator may have left it unescaped, and both name the
// same revision once written back that way. The snapshot stays the bare
// {key: document} object (ADR 0110): no counter is persisted.
func revisionOf(raw json.RawMessage) coredocstore.RevisionValue {
	var compact bytes.Buffer
	//: stored JSON was checked when it was loaded or written, so this holds;
	//: were it not JSON, its bytes as they are still name it.
	if json.Compact(&compact, raw) != nil {
		compact.Reset()
		compact.Write(raw)
	}
	var marshalled bytes.Buffer
	json.HTMLEscape(&marshalled, compact.Bytes())
	sum := sha256.Sum256(marshalled.Bytes())
	//: the token: opaque, compared only for equality.
	return coredocstore.RevisionValue(hex.EncodeToString(sum[:]))
}
