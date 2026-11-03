package docstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// redactedKey stands for a map key in a rendered path: the key is data.
const redactedKey string = "[key]"

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
	// versions holds each document's versions under its key, when the store
	// keeps them, and is nil otherwise. A document stored before the store
	// kept versions, and not versioned since, has none here. A record is
	// never changed once stored: a write stores a new one.
	versions map[string]*versionsRecord
	// weight is what versions weigh, moved by every change of versions
	// (setVersions), so asking reads no record.
	weight coredocstore.VersionWeightValue
	// pending names the overlay entries the snapshot does not contain yet.
	pending map[string]struct{}
	// clock stamps each version.
	clock clock.Clock
	// held reports a document whose versions no write may prune; nil holds
	// nothing.
	held func(key string) bool
	// foldErr is the last automatic fold's failure, until a fold succeeds.
	foldErr error
	// lingering is a batch whose keys' entries are not all written, or whose
	// group entry is not removed: it is completed before anything else is
	// published, or folded. Guarded by the writers' lock.
	lingering *batchPlan
	// path is the snapshot's name, overlay the overlay directory's, and
	// versionsPath the versions file's.
	path, overlay, versionsPath string
	// indexes are the secondary indexes, in declaration order.
	indexes []*index[T]
	// onWrite and onDelete are the hooks called after a write or a deletion.
	onWrite, onDelete hooks
	// writing serialises writers, folds and Close; they hold it across the
	// filesystem work.
	writing sync.Mutex
	// mu guards docs, versions, weight, pending, the index maps, closed, folds
	// and foldErr: changed under writing AND mu, read under either.
	mu sync.RWMutex
	// foldAt is the configured fold threshold.
	foldAt int
	// keep is how many former versions each document keeps; zero keeps no
	// versions at all.
	keep int
	// folds counts the folds since Open.
	folds int
	// closed refuses every call once Close ran.
	closed bool
}

// StatsValue is what a store can say about itself: how many documents it
// holds, how far its overlay has grown, how its folds have gone, and what
// its versions weigh.
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
	// Versions is what the store's versions weigh — its former versions, its
	// trashed documents and their bytes —, a counter every write moves:
	// zero on a store that keeps none (ADR 0143).
	Versions coredocstore.VersionWeightValue
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

// Keys yields the key of every document the store holds that sorts after
// after — every key when after is empty — in key order, and no document.
// The keys are taken once, under the read lock, when the range starts:
// a write made during the range is not seen by it. What it costs is the
// keys' sort, never a document's copy. A closed store yields
// StoreClosed once.
func (s *Store[T]) Keys(after string) iter.Seq2[string, error] {
	//: nothing is read until the range starts.
	return func(yield func(string, error) bool) {
		keys, err := s.keysAfter(after)
		yieldKeys(yield, keys, err)
	}
}

// keysAfter is the sorted keys above after, taken under the read lock: the
// map's own strings, so no key and no document is copied. A closed store
// has none, and says StoreClosed.
func (s *Store[T]) keysAfter(after string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	//: a closed store has nothing to hand out.
	if s.closed {
		//: StoreClosed.
		return nil, kerrs.Wrap(coredocstore.StoreClosed, kerrs.WrapParams{})
	}
	keys := make([]string, 0, len(s.docs))
	//: every key above after — the empty after is below every key, since a
	//: key is never empty.
	for key := range s.docs {
		//: past the resume point.
		if key > after {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	//: open.
	return keys, nil
}

// yieldKeys yields keys in order, then failure when there is one, and reports
// whether the caller wants more: false once it stopped, and after a failure
// whatever it answered to it — the failure is a listing's last yield. Both
// engines' Keys yield through it.
func yieldKeys(yield func(string, error) bool, keys []string, failure error) bool {
	//: in key order, until the caller stops.
	for _, key := range keys {
		//: the caller stopped.
		if !yield(key, nil) {
			//: no more.
			return false
		}
	}
	//: no failure: the caller may want the next page.
	if failure == nil {
		//: more.
		return true
	}
	//: the failure, once, with an empty key; the caller's answer is moot.
	return yield("", failure) && failure == nil
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
	return StatsValue{FoldError: s.foldErr, Documents: len(s.docs), Pending: len(s.pending), Folds: s.folds, Versions: s.weight}
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
			kerrs.String("store", store), kerrs.String("cause", jsonCause(err, reflect.TypeFor[T]())))
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

// jsonCause describes a failure to decode a value of type root without a
// byte of it: encoding/json quotes the offending character of a syntax error,
// and its path to a value that does not fit runs through every map key on the
// way — a store key, a metadata key — which is data that must not travel in an
// error either (#1061).
func jsonCause(err error, root reflect.Type) string {
	//: a value that does not fit its field: the field and the Go type only —
	//: Value is left out, because encoding/json writes a number's digits there.
	if typeErr, isTypeErr := errors.AsType[*json.UnmarshalTypeError](err); isTypeErr {
		typeName := "an unknown type"
		//: the Go type, when the decoder knew it.
		if typeErr.Type != nil {
			typeName = typeErr.Type.String()
		}
		//: the value itself, at the root.
		if typeErr.Field == "" {
			//: the type, which is the program's.
			return "the stored value does not fit " + typeName
		}
		//: the path with every map key redacted, and the type: the program's.
		return "the stored value of field " + fieldPath(root, typeErr.Field) + " does not fit " + typeName
	}
	//: malformed JSON: where, never what.
	if syntaxErr, isSyntaxErr := errors.AsType[*json.SyntaxError](err); isSyntaxErr {
		//: the offset alone.
		return "malformed JSON at offset " + strconv.FormatInt(syntaxErr.Offset, 10)
	}
	//: anything else, by kind.
	return fmt.Sprintf("%T", err)
}

// fieldPath renders dotted — encoding/json's path from a value of type root to
// the one that did not fit, struct field names and map keys joined by dots —
// keeping each struct field's JSON name, which is the program's, and writing
// every map key as [key]. It resolves the path against root's type, so a key
// holding a dot is still one key; a path it cannot resolve is "…", and never
// the path itself.
func fieldPath(root reflect.Type, dotted string) string {
	rendered, resolved := resolvePath(root, strings.Split(dotted, "."))
	//: a path the type does not explain says nothing at all.
	if !resolved {
		//: no segment of it.
		return "…"
	}
	//: field names and [key], nothing else.
	return rendered
}

// resolvePath renders segments under t and reports whether they resolve to a
// value of t exactly — every segment a struct field's JSON name or part of a
// map key, none left over. Pointers, slices and arrays add no segment.
func resolvePath(t reflect.Type, segments []string) (rendered string, resolved bool) {
	//: encoding/json names no element of a slice and no pointer.
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	//: the whole path consumed.
	if len(segments) == 0 {
		//: nothing more to render.
		return "", true
	}
	switch t.Kind() {
	//: a struct field, by its JSON name — which may itself hold a dot.
	case reflect.Struct:
		//: the field the next segments name.
		return resolveField(t, segments)
	//: a map key, data, over as many segments as leave a path the element
	//: type resolves.
	case reflect.Map:
		//: the key, redacted.
		return resolveMapKey(t, segments)
	//: below an interface, every segment is a key of decoded data.
	case reflect.Interface:
		//: none of them.
		return "…", true
	//: a leaf with segments left: not this type's path.
	default:
		//: unresolved.
		return "", false
	}
}

// resolveField renders segments under the struct type t: the field whose JSON
// name the next segments spell, then the rest under the field's type.
func resolveField(t reflect.Type, segments []string) (rendered string, resolved bool) {
	//: every field the decoder could have descended into.
	for _, field := range jsonFields(t) {
		n := strings.Count(field.name, ".") + 1
		//: the field's name is the next segments.
		if n > len(segments) || strings.Join(segments[:n], ".") != field.name {
			continue
		}
		//: the rest resolved under the field's type.
		if rest, ok := resolvePath(field.typ, segments[n:]); ok {
			//: the field's name, the program's.
			return joinPath(field.name, rest), true
		}
	}
	//: no field explains the path.
	return "", false
}

// resolveMapKey renders segments under the map type t: a key, over as many
// segments as leave a path the element type resolves, shortest first.
func resolveMapKey(t reflect.Type, segments []string) (rendered string, resolved bool) {
	//: the key's shortest reading first.
	for taken := 1; taken <= len(segments); taken++ {
		//: the rest resolved under the element type.
		if rest, ok := resolvePath(t.Elem(), segments[taken:]); ok {
			//: the key, redacted.
			return joinPath(redactedKey, rest), true
		}
	}
	//: no reading of the key explains the path.
	return "", false
}

// joinPath joins a rendered segment and the rendering of the rest.
func joinPath(head, rest string) string {
	//: the last segment.
	if rest == "" {
		//: alone.
		return head
	}
	//: dotted, as encoding/json writes it.
	return head + "." + rest
}

// jsonField is a struct field as encoding/json names it.
type jsonField struct {
	// typ is the field's type.
	typ reflect.Type
	// name is its JSON name.
	name string
}

// jsonFields lists the fields encoding/json decodes into t — exported or
// embedded, under their tag's name or their own, "-" skipped — an untagged
// embedded struct's fields promoted.
func jsonFields(t reflect.Type) []jsonField {
	var out []jsonField
	//: in declaration order.
	for field := range t.Fields() {
		//: an untagged embedded struct: its fields are this struct's.
		if promoted, isPromoted := promotedStruct(field); isPromoted {
			out = append(out, jsonFields(promoted)...)
			continue
		}
		//: a field the decoder fills, under its JSON name.
		if name, decoded := jsonNameOf(field); decoded {
			out = append(out, jsonField{name: name, typ: field.Type})
		}
	}
	//: every field.
	return out
}

// promotedStruct returns the struct whose fields an untagged embedded field
// promotes — through a pointer too —, and whether field is one.
func promotedStruct(field reflect.StructField) (promoted reflect.Type, isPromoted bool) {
	tag := field.Tag.Get("json")
	name, _, _ := strings.Cut(tag, ",")
	promoted = field.Type
	//: a pointer to an embedded struct promotes the same way.
	if promoted.Kind() == reflect.Pointer {
		promoted = promoted.Elem()
	}
	//: embedded, neither excluded nor renamed, and a struct.
	return promoted, field.Anonymous && tag != "-" && name == "" && promoted.Kind() == reflect.Struct
}

// jsonNameOf returns the name encoding/json decodes field under — its tag's,
// else its Go name — and whether it decodes into it at all: an unexported
// field, embedded or not, and one tagged "-" are never decoded into.
func jsonNameOf(field reflect.StructField) (name string, decoded bool) {
	tag := field.Tag.Get("json")
	//: unexported, or excluded: never decoded into.
	if !field.IsExported() || tag == "-" {
		//: not a field of the document.
		return "", false
	}
	name, _, _ = strings.Cut(tag, ",")
	//: the Go name, when the tag gives none.
	if name == "" {
		name = field.Name
	}
	//: decoded under name.
	return name, true
}

// sortedKeys returns m's keys in order.
func sortedKeys[V any](m map[string]V) []string {
	//: sorted, so every listing is the same listing.
	return slices.Sorted(maps.Keys(m))
}
