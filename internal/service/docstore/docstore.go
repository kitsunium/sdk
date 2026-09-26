// Package docstore is a typed, keyed store of JSON documents with secondary
// indexes, kept in memory and, when given a filesystem, persisted to it —
// every write durable before it returns. ADR 0110.
//
// # What it is
//
// A Store[T] holds documents of one Go type, each under the key a function of
// the document returns. Every read returns a copy and every write stores one:
// a document is kept as its JSON encoding, so what a caller holds can never
// alias what the store holds, and the memory and file backends behave alike.
// Writes come in three modes — Put creates or replaces, Insert refuses an
// existing key, Replace refuses a missing one and never resurrects a deleted
// document — plus Update, a read-modify-write, and Delete.
//
// Secondary indexes are declared at Open: Unique files at most one document
// per key and refuses a write that would file a second, Index files any
// number. Lookup reads a unique index, Find reads any index, Filter reads
// every document. The indexes are rebuilt when the store opens, and a load
// whose documents break a unique index is refused rather than served.
//
// # How it persists, and what that costs
//
// The resting state is ONE file, the snapshot: a JSON object mapping each key
// to its document. A write does not rewrite it. It publishes one small file —
// an overlay entry, named by the SHA-256 of the key, holding the key and its
// document or its deletion — atomically, through vfs.AtomicWriter, before it
// returns. A write therefore costs what one document costs, whatever the
// store holds. When the overlay holds as many entries as the store holds
// documents (at least DefaultFoldAt), the write that reaches that count folds
// it: the snapshot is rewritten once and the entries it now contains are
// removed. Folding N documents once per N writes keeps the average write a
// constant. Close folds, so a store that was closed rests as a single
// snapshot a person can read.
//
// Loading reads the snapshot and replays the overlay on top of it. Replay
// order does not matter — one entry per key, each holding that key's whole
// latest state — and an entry the last fold already folded holds exactly what
// the snapshot holds, so a crash anywhere in a fold, or between a fold and
// the removals that follow it, loads the same documents.
//
// # Concurrency
//
// Readers never wait for a disk. Writers are serialised by one lock and do
// their filesystem work under it alone; the documents and the indexes are
// changed only once the write is durable, under a second lock readers share.
// A reader sees the state before the write or after it, never a document and
// an index that disagree.
//
// # What it is not
//
// One process. The store detects no second process opening the same files,
// and two would each believe their own memory. It keeps every document in
// memory and sorts the keys for every List. It has no transaction across
// documents. It is a store for the documents a service owns, not a database.
package docstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"sync"

	corevfs "github.com/kitsunium/sdk/internal/core/vfs"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// Store is a typed, keyed collection of JSON documents with secondary
// indexes. Build one with [Open]. It is safe for concurrent use.
type Store[T any] struct {
	// key is the configured key function.
	key func(T) string
	// fs is where the store persists; nil in memory.
	fs corevfs.FullFS
	// byName finds an index by its name. It and indexes never change after
	// Open.
	byName map[string]*index[T]
	// docs holds each document's JSON under its key — the snapshot's own
	// shape, so a fold encodes it as it is.
	docs map[string]json.RawMessage
	// pending names the overlay entries the snapshot does not contain yet.
	pending map[string]struct{}
	// foldErr is the last automatic fold's failure, until a fold succeeds.
	foldErr error
	// path is the snapshot's name; overlay the overlay directory's.
	path, overlay string
	// indexes are the secondary indexes, in declaration order.
	indexes []*index[T]
	// onWrite and onDelete are the hooks called after a write or a deletion.
	onWrite, onDelete hooks
	// writing serialises writers, folds and Close; they hold it across the
	// filesystem work.
	writing sync.Mutex
	// mu guards docs, pending, the index maps, closed, folds and foldErr:
	// changed under writing AND mu, read under either.
	mu sync.RWMutex
	// foldAt is the configured fold threshold.
	foldAt int
	// folds counts the folds since Open.
	folds int
	// closed refuses every call once Close ran.
	closed bool
}

// EntryValue is one stored document as the store holds it: its key and its
// JSON, for a caller that shows documents rather than decoding them.
type EntryValue struct {
	// Key is the document's key.
	Key string
	// JSON is the document, a copy the caller owns.
	JSON json.RawMessage
}

// StatsValue is what a store can say about itself: how many documents it
// holds, how far its overlay has grown, and how its folds have gone.
type StatsValue struct {
	// FoldError is the last automatic fold's failure, nil once a fold
	// succeeds. The write that triggered the fold succeeded either way — its
	// own entry was already durable — and the entries stay until a fold works.
	FoldError error
	// Documents is how many documents the store holds.
	Documents int
	// Pending is how many overlay entries the snapshot does not contain yet:
	// zero in memory, and after a fold.
	Pending int
	// Folds is how many folds ran since the store opened, Open's own included.
	Folds int
}

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
		return zero, kerrs.Wrap(StoreClosed, kerrs.WrapParams{})
	}
	//: a miss.
	if !found {
		//: DocumentNotFound, naming no key.
		return zero, kerrs.Wrap(DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.path))
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
		return nil, kerrs.Wrap(StoreClosed, kerrs.WrapParams{})
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
func (s *Store[T]) Entries(limit int) ([]EntryValue, error) {
	entries, closed := s.snapshotEntries(limit)
	//: a closed store answers nothing.
	if closed {
		//: StoreClosed.
		return nil, kerrs.Wrap(StoreClosed, kerrs.WrapParams{})
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
		return zero, kerrs.Wrap(IndexUnknown, kerrs.WrapParams{}, kerrs.String("index", index))
	}
	//: one document is only meaningful from a unique index.
	if !ix.unique {
		//: IndexNotUnique, naming the index.
		return zero, kerrs.Wrap(IndexNotUnique, kerrs.WrapParams{}, kerrs.String("index", index))
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
		return zero, kerrs.Wrap(StoreClosed, kerrs.WrapParams{})
	}
	//: nobody holds the key. The key is not quoted: an index key is often
	//: what a caller must not learn back — an e-mail, the hash of a token.
	if raw == nil {
		//: DocumentNotFound, naming the index.
		return zero, kerrs.Wrap(DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.path), kerrs.String("index", index))
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
		return nil, kerrs.Wrap(IndexUnknown, kerrs.WrapParams{}, kerrs.String("index", index))
	}
	s.mu.RLock()
	owners, closed := ix.holders(key), s.closed
	entries := make([]EntryValue, len(owners))
	//: each holder's JSON, taken under the lock.
	for i, owner := range owners {
		entries[i] = EntryValue{Key: owner, JSON: s.docs[owner]}
	}
	s.mu.RUnlock()
	//: a closed store answers nothing.
	if closed {
		//: StoreClosed.
		return nil, kerrs.Wrap(StoreClosed, kerrs.WrapParams{})
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
func (s *Store[T]) snapshotEntries(limit int) (entries []EntryValue, closed bool) {
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
	entries = make([]EntryValue, len(keys))
	//: each document's JSON under its key.
	for i, key := range keys {
		entries[i] = EntryValue{Key: key, JSON: s.docs[key]}
	}
	//: open.
	return entries, false
}

// decode decodes one stored document into a fresh value.
func (s *Store[T]) decode(raw json.RawMessage) (T, error) {
	var v T
	//: the type changed under data an older program wrote.
	if err := json.Unmarshal(raw, &v); err != nil {
		var zero T
		//: DocumentUndecodable; what failed, never the document and never
		//: the key.
		return zero, kerrs.Wrap(DocumentUndecodable, kerrs.WrapParams{},
			kerrs.String("store", s.path), kerrs.String("cause", jsonCause(err)))
	}
	//: the caller's own copy.
	return v, nil
}

// decodeAll decodes entries in order. It never returns nil for no entries.
func (s *Store[T]) decodeAll(entries []EntryValue) ([]T, error) {
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
