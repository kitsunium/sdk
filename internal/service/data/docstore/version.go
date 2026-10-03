// Package docstore — a document's versions: the current one, which is the
// document itself, and the former ones kept beside it, recorded and pruned in
// the write that stores the document (ADR 0143).
package docstore

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"time"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// versionsRecord is one document's versions as the file store keeps them in
// memory and in its files: the current version without its document — the
// document is the document itself — and the former versions, newest first.
// A record is never changed once stored: a write stores a new one.
type versionsRecord struct {
	// Former are the former versions, newest first.
	Former []formerVersion `json:"former,omitempty"`
	// Current is the current version's number, instant and metadata.
	Current versionHead `json:"current"`
}

// versionHead is a version without its document.
type versionHead struct {
	// At is when the write that made it ran; zero when unknown.
	At time.Time `json:"at,omitzero"`
	// Meta is what that write's caller said about it.
	Meta map[string]string `json:"meta,omitempty"`
	// Number is its number, from 1.
	Number uint64 `json:"number"`
}

// formerVersion is a former version: a version and the document it holds.
type formerVersion struct {
	// At is when the write that made it ran; zero when unknown.
	At time.Time `json:"at,omitzero"`
	// Meta is what that write's caller said about it.
	Meta map[string]string `json:"meta,omitempty"`
	// Document is the document the version holds, compact.
	Document json.RawMessage `json:"doc"`
	// Number is its number, from 1.
	Number uint64 `json:"number"`
}

// firstHead is the current version of a document stored before its store
// kept versions: version 1, made at an instant nobody recorded.
var firstHead = versionHead{Number: 1}

// PutStamped is Put, with what the write says about the version it makes.
func (s *Store[T]) PutStamped(v T, stamp coredocstore.StampValue) error {
	//: anything under the key is fine.
	return s.store(v, upsert, stamp)
}

// InsertStamped is Insert, with what the write says about the version it
// makes: version 1, whatever stamp.InPlace says.
func (s *Store[T]) InsertStamped(v T, stamp coredocstore.StampValue) error {
	//: nothing may be under the key.
	return s.store(v, insertOnly, stamp)
}

// ReplaceStamped is Replace, with what the write says about the version it
// makes.
func (s *Store[T]) ReplaceStamped(v T, stamp coredocstore.StampValue) error {
	//: a document must be under the key.
	return s.store(v, replaceOnly, stamp)
}

// UpdateStamped is Update, with what the write says about the version it
// makes.
func (s *Store[T]) UpdateStamped(key string, stamp coredocstore.StampValue, fn func(*T) error) (T, error) {
	v, applied, err := s.modify(key, stamp, fn)
	//: a write that took effect is announced, WriteUnconfirmed included.
	if applied {
		s.onWrite.call(key)
	}
	//: the stored result, or the zero value.
	return v, err
}

// Versions returns the versions the store keeps of the document stored under
// key, newest first: the current one — the document as it is now — then the
// former ones. It returns DocumentNotFound when no document is stored under
// key, and VersionsNotKept when the store keeps no versions.
func (s *Store[T]) Versions(key string) ([]coredocstore.VersionValue, error) {
	//: a store that keeps none has none to read.
	if s.keep == 0 {
		//: VersionsNotKept, naming the store.
		return nil, versionsNotKept(s.path)
	}
	s.mu.RLock()
	raw, found := s.docs[key]
	record, closed := s.versions[key], s.closed
	s.mu.RUnlock()
	//: a closed store answers nothing.
	if closed {
		//: StoreClosed.
		return nil, kerrs.Wrap(coredocstore.StoreClosed, kerrs.WrapParams{})
	}
	//: no document, so no version either.
	if !found {
		//: DocumentNotFound, naming no key.
		return nil, kerrs.Wrap(coredocstore.DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.path))
	}
	//: copies: a record is never changed once stored, so reading it outside
	//: the lock is reading what it held when it was taken.
	return record.list(raw), nil
}

// Version returns the version numbered number of the document stored under
// key, VersionNotFound when the document keeps none of that number — never
// made, or pruned — and what Versions returns otherwise.
func (s *Store[T]) Version(key string, number uint64) (coredocstore.VersionValue, error) {
	all, err := s.Versions(key)
	//: StoreClosed, DocumentNotFound or VersionsNotKept.
	if err != nil {
		//: nothing read.
		return coredocstore.VersionValue{}, err
	}
	//: the one asked for, or a miss.
	return pickVersion(s.path, all, number)
}

// RewriteVersions rewrites the former versions of the document stored under
// key — every version but the current one, which is the document and which
// only a write changes — in one durable write, under the writers' lock. fn
// receives copies of them, newest first, and returns those to keep, in the
// same order: it may drop a version, and change the document one holds and
// what its writer said, and nothing else — VersionsRewriteRefused for a
// version it was not given, out of order, made at another instant or that is
// not JSON. An error from fn is returned as it is and changes nothing. It
// calls no hook: the document did not change.
//
// It is what an erasure needs: the former versions cleared as the document
// is, or dropped. fn runs under the writers' lock, like Update's.
func (s *Store[T]) RewriteVersions(key string, fn func(former []coredocstore.VersionValue) ([]coredocstore.VersionValue, error)) error {
	//: a store that keeps none has none to rewrite.
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
	raw, found := s.docs[key]
	//: no document, so no version either.
	if !found {
		//: DocumentNotFound, naming no key.
		return kerrs.Wrap(coredocstore.DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.path))
	}
	record := s.versions[key]
	next, err := rewriteRecord(s.path, record, fn)
	//: fn's own error, or what it returned refused.
	if err != nil {
		//: nothing changed.
		return err
	}
	persistErr := s.persist(key, raw, false, next)
	//: the filesystem refused: nothing changed anywhere.
	if persistErr != nil && !kerrs.HasCode(persistErr, coredocstore.CodeWriteUnconfirmed) {
		//: PersistFailed.
		return persistErr
	}
	s.mu.Lock()
	s.setVersions(key, next)
	s.markPending(key)
	s.mu.Unlock()
	s.maybeFold()
	//: rewritten, and nil or WriteUnconfirmed.
	return persistErr
}

// nextVersions returns the versions a write storing raw under key leaves: a
// creation is version 1; a write that changes the document makes the next
// version, the current one becoming the newest former one; a write stamped
// InPlace, or storing the JSON already stored, makes none. Either way the
// former versions beyond what the store keeps are pruned, unless Held says
// the document is held. It returns nil for a store that keeps no versions,
// and for a document stored before it kept them that no write has versioned
// since. The caller holds the writers' lock.
func (s *Store[T]) nextVersions(key string, raw json.RawMessage, stamp coredocstore.StampValue) *versionsRecord {
	//: a store that keeps none records none.
	if s.keep == 0 {
		//: no versions.
		return nil
	}
	old, existed := s.docs[key]
	record := s.versions[key]
	//: a creation is version 1, whatever the stamp says.
	if !existed {
		//: the first version, the document itself.
		return &versionsRecord{Current: versionHead{At: s.now(), Meta: maps.Clone(stamp.Meta), Number: 1}}
	}
	//: the caller said the write makes no version.
	if stamp.InPlace {
		//: the same versions, pruned if a hold was lifted or Versions lowered.
		return s.pruned(key, record)
	}
	former := compactJSON(old)
	//: the write stores what is already stored: a snapshot read back holds
	//: its documents indented, so the comparison is made compact.
	if bytes.Equal(former, raw) {
		//: the same versions, pruned all the same.
		return s.pruned(key, record)
	}
	head, kept := firstHead, []formerVersion(nil)
	//: a document versioned before; otherwise the store held it before it
	//: kept versions, and it is version 1.
	if record != nil {
		head, kept = record.Current, record.Former
	}
	next := &versionsRecord{
		Current: versionHead{At: s.now(), Meta: maps.Clone(stamp.Meta), Number: head.Number + 1},
		Former: append([]formerVersion{{At: head.At, Meta: head.Meta, Document: former, Number: head.Number}},
			kept...),
	}
	//: pruned in this very write.
	return s.pruned(key, next)
}

// pruned returns record with at most as many former versions as the store
// keeps — the newest — unless Held says the document is held: its versions
// then pile up until a write finds it released. The caller holds the writers'
// lock.
func (s *Store[T]) pruned(key string, record *versionsRecord) *versionsRecord {
	//: nothing beyond what the store keeps, or a legal hold keeping every
	//: version — asked only when there is something to prune.
	if record == nil || len(record.Former) <= s.keep || (s.held != nil && s.held(key)) {
		//: as it is.
		return record
	}
	//: the newest ones, in a record — and an array — of their own, so the
	//: pruned documents are nobody's to keep in memory.
	return &versionsRecord{Current: record.Current, Former: slices.Clone(record.Former[:s.keep])}
}

// setVersions stores record as key's versions. The caller holds both locks.
func (s *Store[T]) setVersions(key string, record *versionsRecord) {
	//: a store that keeps none has no map.
	if s.keep == 0 {
		return
	}
	//: none left, or a document the store held before it kept versions.
	if record == nil {
		delete(s.versions, key)
		return
	}
	s.versions[key] = record
}

// now is the instant a version is made at, in UTC, without the monotonic
// reading a clock may carry.
func (s *Store[T]) now() time.Time {
	//: UTC strips the monotonic reading.
	return s.clock.Now().UTC()
}

// list returns the versions of a document whose current JSON is current,
// newest first, as copies the caller owns. A nil record is a document stored
// before its store kept versions: its one version is the document.
func (r *versionsRecord) list(current json.RawMessage) []coredocstore.VersionValue {
	head, former := firstHead, []formerVersion(nil)
	//: versioned since.
	if r != nil {
		head, former = r.Current, r.Former
	}
	out := make([]coredocstore.VersionValue, 0, 1+len(former))
	out = append(out, coredocstore.VersionValue{At: head.At, Meta: maps.Clone(head.Meta), JSON: compactJSON(current), Number: head.Number})
	//: newest first.
	for _, f := range former {
		out = append(out, coredocstore.VersionValue{At: f.At, Meta: maps.Clone(f.Meta), JSON: slices.Clone(f.Document), Number: f.Number})
	}
	//: every version kept.
	return out
}

// rewriteRecord applies a rewrite's function to the former versions of
// record, and returns the record it leaves: the same current version, and the
// former versions fn kept, checked. A nil record has no former version.
func rewriteRecord(store string, record *versionsRecord, fn func([]coredocstore.VersionValue) ([]coredocstore.VersionValue, error)) (*versionsRecord, error) {
	var given []formerVersion
	//: a document versioned since its store kept versions.
	if record != nil {
		given = record.Former
	}
	copies := make([]coredocstore.VersionValue, len(given))
	//: copies, so fn cannot reach what the store holds.
	for i, f := range given {
		copies[i] = coredocstore.VersionValue{At: f.At, Meta: maps.Clone(f.Meta), JSON: slices.Clone(f.Document), Number: f.Number}
	}
	kept, err := fn(copies)
	//: the caller's own error travels untouched.
	if err != nil {
		//: nothing changed.
		return nil, err
	}
	former, err := checkRewrite(store, given, kept)
	//: VersionsRewriteRefused.
	if err != nil {
		//: nothing changed.
		return nil, err
	}
	//: a document with no version recorded keeps none.
	if record == nil {
		//: nothing to rewrite, and fn returned nothing: checkRewrite saw to it.
		return nil, nil
	}
	//: the current version as it was, the former ones as fn left them.
	return &versionsRecord{Current: record.Current, Former: former}, nil
}

// checkRewrite checks what a rewrite's function returned against the former
// versions it was given, and returns them as the store keeps them: each one
// of those it was given, in the same order, with its own number and instant,
// and a document that is JSON, compacted and HTML-escaped as json.Marshal
// writes one. Both engines check through it.
func checkRewrite(store string, given []formerVersion, kept []coredocstore.VersionValue) ([]formerVersion, error) {
	out := make([]formerVersion, 0, len(kept))
	next := 0
	//: in the order fn returned them.
	for _, v := range kept {
		at := slices.IndexFunc(given[next:], func(f formerVersion) bool { return f.Number == v.Number })
		//: a number it was not given, or one it was given before another.
		if at < 0 {
			//: VersionsRewriteRefused, naming the problem.
			return nil, rewriteRefused(store, "a version it was not given, or out of order")
		}
		original := given[next+at]
		next += at + 1
		//: when a version was made is the store's record, not fn's.
		if !v.At.Equal(original.At) {
			//: VersionsRewriteRefused, naming the problem.
			return nil, rewriteRefused(store, "a version made at another instant")
		}
		var compact, escaped bytes.Buffer
		//: a version holds a JSON document.
		if len(v.JSON) == 0 || json.Compact(&compact, v.JSON) != nil {
			//: VersionsRewriteRefused, naming the problem and never the JSON.
			return nil, rewriteRefused(store, "a version that is not JSON")
		}
		//: escaped as json.Marshal escapes a document — <, >, & — so the
		//: bytes read back are the same before a restart and after one, and
		//: on either engine.
		json.HTMLEscape(&escaped, compact.Bytes())
		out = append(out, formerVersion{At: original.At, Meta: maps.Clone(v.Meta), Document: escaped.Bytes(), Number: original.Number})
	}
	//: the versions to keep.
	return out, nil
}

// pickVersion returns the version numbered number among all, or
// VersionNotFound naming store and the number.
func pickVersion(store string, all []coredocstore.VersionValue, number uint64) (coredocstore.VersionValue, error) {
	//: newest first; a document keeps few.
	for _, v := range all {
		//: the one asked for.
		if v.Number == number {
			//: found.
			return v, nil
		}
	}
	//: never made, or pruned.
	return coredocstore.VersionValue{}, kerrs.Wrap(coredocstore.VersionNotFound, kerrs.WrapParams{},
		kerrs.String("store", store), kerrs.String("number", strconv.FormatUint(number, 10)))
}

// compactJSON returns raw without the spaces a snapshot's indentation puts in
// it, so that a document read back from the files compares with one just
// encoded. raw is JSON the store encoded or checked; were it not, it is
// returned as it is.
func compactJSON(raw json.RawMessage) json.RawMessage {
	var out bytes.Buffer
	//: never fails on JSON the store holds.
	if err := json.Compact(&out, raw); err != nil {
		//: a copy, as it is.
		return slices.Clone(raw)
	}
	//: compact.
	return out.Bytes()
}

// versionsNotKept is VersionsNotKept naming store.
func versionsNotKept(store string) error {
	//: the store, never a key.
	return kerrs.Wrap(coredocstore.VersionsNotKept, kerrs.WrapParams{}, kerrs.String("store", store))
}

// rewriteRefused is VersionsRewriteRefused naming store and the problem.
func rewriteRefused(store, problem string) error {
	//: the problem, never a version.
	return kerrs.Wrap(coredocstore.VersionsRewriteRefused, kerrs.WrapParams{}, kerrs.String("store", store), kerrs.String("problem", problem))
}
