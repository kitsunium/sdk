package docstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// Get returns the document stored under key, or DocumentNotFound.
func (s *Store[T]) Get(key string) (T, error) {
	var zero T
	s.mu.RLock()
	raw, found := s.docs[key]
	closed := s.closed
	s.mu.RUnlock()
	//: a closed store answers nothing.
	if closed {
		//: StoreClosed.
		return zero, kerrs.Wrap(coredocstore.StoreClosed, kerrs.WrapParams{})
	}
	//: a miss.
	if !found {
		//: DocumentNotFound, naming no key.
		return zero, kerrs.Wrap(coredocstore.DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.path))
	}
	//: a copy of the stored document.
	return s.decode(raw)
}

// List returns every document, in key order.
func (s *Store[T]) List() ([]T, error) {
	entries, closed := s.snapshotEntries(0)
	//: a closed store answers nothing.
	if closed {
		//: StoreClosed.
		return nil, kerrs.Wrap(coredocstore.StoreClosed, kerrs.WrapParams{})
	}
	//: decoded outside the lock.
	return s.decodeAll(entries)
}

// Filter returns the documents keep accepts, in key order. It decodes every
// document; an index is the way not to.
func (s *Store[T]) Filter(keep func(T) bool) ([]T, error) {
	all, err := s.List()
	//: StoreClosed or DocumentUndecodable.
	if err != nil {
		//: nothing filtered.
		return nil, err
	}
	out := all[:0]
	//: in key order.
	for _, v := range all {
		//: the caller's predicate.
		if keep(v) {
			out = append(out, v)
		}
	}
	//: the accepted documents, never nil.
	return out, nil
}

// Entries returns up to limit stored documents as JSON, in key order — every
// one when limit is not positive — for a caller that shows documents rather
// than decoding them. Each JSON is the caller's own copy.
func (s *Store[T]) Entries(limit int) ([]coredocstore.EntryValue, error) {
	entries, closed := s.snapshotEntries(limit)
	//: a closed store answers nothing.
	if closed {
		//: StoreClosed.
		return nil, kerrs.Wrap(coredocstore.StoreClosed, kerrs.WrapParams{})
	}
	//: copies, so a caller's edit never reaches the store.
	for i := range entries {
		entries[i].JSON = slices.Clone(entries[i].JSON)
	}
	//: the first limit documents.
	return entries, nil
}

// Lookup returns the document holding key in the unique index named index,
// DocumentNotFound when none does, IndexUnknown for an undeclared index and
// IndexNotUnique for an index that may hold several: read that one with Find.
func (s *Store[T]) Lookup(index, key string) (T, error) {
	var zero T
	ix, found := s.byName[index]
	//: an index the store never declared.
	if !found {
		//: IndexUnknown, naming the index.
		return zero, kerrs.Wrap(coredocstore.IndexUnknown, kerrs.WrapParams{}, kerrs.String("index", index))
	}
	//: one document is only meaningful from a unique index.
	if !ix.unique {
		//: IndexNotUnique, naming the index.
		return zero, kerrs.Wrap(coredocstore.IndexNotUnique, kerrs.WrapParams{}, kerrs.String("index", index))
	}
	s.mu.RLock()
	owners, closed := ix.holders(key), s.closed
	var raw json.RawMessage
	//: a unique index holds at most one.
	if len(owners) == 1 {
		raw = s.docs[owners[0]]
	}
	s.mu.RUnlock()
	//: a closed store answers nothing.
	if closed {
		//: StoreClosed.
		return zero, kerrs.Wrap(coredocstore.StoreClosed, kerrs.WrapParams{})
	}
	//: nobody holds the key. The key is not quoted: an index key is often
	//: what a caller must not learn back — an e-mail, the hash of a token.
	if raw == nil {
		//: DocumentNotFound, naming the index.
		return zero, kerrs.Wrap(coredocstore.DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.path), kerrs.String("index", index))
	}
	//: a copy of the holder.
	return s.decode(raw)
}

// Find returns the documents filed under key in the index named index, in
// store-key order. It reads any index, unique or not, and no document is an
// empty slice, not an error.
func (s *Store[T]) Find(index, key string) ([]T, error) {
	ix, found := s.byName[index]
	//: an index the store never declared.
	if !found {
		//: IndexUnknown, naming the index.
		return nil, kerrs.Wrap(coredocstore.IndexUnknown, kerrs.WrapParams{}, kerrs.String("index", index))
	}
	s.mu.RLock()
	owners, closed := ix.holders(key), s.closed
	entries := make([]coredocstore.EntryValue, len(owners))
	//: each holder's JSON, taken under the lock.
	for i, owner := range owners {
		entries[i] = coredocstore.EntryValue{Key: owner, JSON: s.docs[owner]}
	}
	s.mu.RUnlock()
	//: a closed store answers nothing.
	if closed {
		//: StoreClosed.
		return nil, kerrs.Wrap(coredocstore.StoreClosed, kerrs.WrapParams{})
	}
	//: decoded outside the lock; empty, never nil.
	return s.decodeAll(entries)
}

// Stats reports what the store holds and where its overlay stands. It answers
// after Close too, with the state the store closed in.
func (s *Store[T]) Stats() StatsValue {
	s.mu.RLock()
	defer s.mu.RUnlock()
	//: one consistent reading.
	return StatsValue{FoldError: s.foldErr, Documents: len(s.docs), Pending: len(s.pending), Folds: s.folds}
}

// snapshotEntries takes up to limit documents, in key order — all of them
// when limit is not positive — and reports whether the store is closed. The
// JSON slices are the store's own: the caller copies what it hands out.
func (s *Store[T]) snapshotEntries(limit int) (entries []coredocstore.EntryValue, closed bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	//: a closed store has nothing to hand out.
	if s.closed {
		//: closed.
		return nil, true
	}
	keys := sortedKeys(s.docs)
	//: a positive limit caps the answer.
	if limit > 0 && limit < len(keys) {
		keys = keys[:limit]
	}
	entries = make([]coredocstore.EntryValue, len(keys))
	//: each document's JSON under its key.
	for i, key := range keys {
		entries[i] = coredocstore.EntryValue{Key: key, JSON: s.docs[key]}
	}
	//: open.
	return entries, false
}

// decode decodes one stored document into a fresh value.
func (s *Store[T]) decode(raw json.RawMessage) (T, error) {
	//: the refusal names the snapshot.
	return decodeAs[T](s.path, raw)
}

// decodeAs decodes one stored document into a fresh value, or refuses it
// naming store — a snapshot's path, or a table — and never a byte of it. Every
// engine decodes through it, so a type that changed under stored data is the
// same refusal in each.
func decodeAs[T any](store string, raw []byte) (T, error) {
	var v T
	//: the type changed under data an older program wrote.
	if err := json.Unmarshal(raw, &v); err != nil {
		var zero T
		//: DocumentUndecodable; what failed, never the document and never
		//: the key.
		return zero, kerrs.Wrap(coredocstore.DocumentUndecodable, kerrs.WrapParams{},
			kerrs.String("store", store), kerrs.String("cause", jsonCause(err)))
	}
	//: the caller's own copy.
	return v, nil
}

// decodeAll decodes entries in order. It never returns nil for no entries.
func (s *Store[T]) decodeAll(entries []coredocstore.EntryValue) ([]T, error) {
	out := make([]T, len(entries))
	//: in the order given.
	for i, entry := range entries {
		v, err := s.decode(entry.JSON)
		//: one undecodable document fails the read.
		if err != nil {
			//: DocumentUndecodable.
			return nil, err
		}
		out[i] = v
	}
	//: every document, decoded.
	return out, nil
}

// jsonCause describes a decoding failure without a byte of the document:
// encoding/json quotes the offending character of a syntax error, and a
// stored document is exactly what must not travel in an error.
func jsonCause(err error) string {
	//: a value that does not fit its field: the field and the Go type only —
	//: Value is left out, because encoding/json writes a number's digits there.
	if typeErr, isTypeErr := errors.AsType[*json.UnmarshalTypeError](err); isTypeErr {
		typeName := "an unknown type"
		//: the Go type, when the decoder knew it.
		if typeErr.Type != nil {
			typeName = typeErr.Type.String()
		}
		//: the path and the type, which are the program's, not the data's.
		return "the stored value of field " + typeErr.Field + " does not fit " + typeName
	}
	//: malformed JSON: where, never what.
	if syntaxErr, isSyntaxErr := errors.AsType[*json.SyntaxError](err); isSyntaxErr {
		//: the offset alone.
		return "malformed JSON at offset " + strconv.FormatInt(syntaxErr.Offset, 10)
	}
	//: anything else, by kind.
	return fmt.Sprintf("%T", err)
}

// sortedKeys returns m's keys in order.
func sortedKeys[V any](m map[string]V) []string {
	//: sorted, so every listing is the same listing.
	return slices.Sorted(maps.Keys(m))
}
