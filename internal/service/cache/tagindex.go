// Package cache — the two-way tag index behind InvalidateTag.
package cache

import (
	"maps"
	"slices"
)

// initialIndexCapacity pre-sizes both index maps and each tag bucket. It is a
// hint, not a bound: a store with no tagged entry must not pay for a large
// allocation, and one with many grows past it in a handful of doublings.
const initialIndexCapacity int = 16

// tagIndex maps tags to the keys carrying them, and back.
//
// # Why two maps and not one
//
// The naive tag invalidation walks the whole cache and drops every entry whose
// tag list contains the tag. That is O(N) in the STORE size for an operation
// whose result is usually a handful of entries, and it puts a full scan under
// the store's write lock — so a 100 000-entry cache stalls every reader to
// delete three things.
//
// byTag answers "which keys carry this tag" in one map lookup, so an
// invalidation costs O(k) in the keys that actually carry the tag.
//
// byKey exists only to keep byTag honest. Without it, an entry evicted by the
// LRU or expired by TTL would leave its key in every one of its tag buckets
// forever: the buckets would grow without bound, and an invalidation would
// spend its time deleting keys that no longer exist. byKey is what makes
// removal O(T) in that key's own tags instead of O(number of tags in the
// store).
//
// # What it costs
//
// Per tagged key: one byKey entry — a string header (16 B) plus a slice header
// (24 B) plus 16 B per tag — and one byTag inner-map entry per (tag, key) pair.
// With Go's map overhead that lands near 100 B per key plus roughly 70 B per
// (tag, key) pair on amd64. The tag TEXT is not duplicated: Go strings are
// immutable, so both maps hold headers pointing at one backing array.
// internal/service/cache/BENCH.md measures the difference between a tagged and
// an untagged Set rather than leaving the estimate unchecked.
//
// Not safe for concurrent use: every method is called with the owning store's
// mutex held.
type tagIndex struct {
	byKey map[string][]string
	byTag map[string]map[string]struct{}
}

// newTagIndex builds an empty index.
func newTagIndex() *tagIndex {
	//: both maps start small; a store with no tagged entry pays two headers.
	return &tagIndex{
		byKey: make(map[string][]string, initialIndexCapacity),
		byTag: make(map[string]map[string]struct{}, initialIndexCapacity),
	}
}

// add indexes key under tags, replacing whatever tags key had before, and
// returns the deduplicated copy the index now owns.
//
// The copy is not politeness: the caller's slice is theirs to reuse or mutate
// after Set returns, and the store keeps this one instead so an entry and its
// index entry are literally the same slice.
func (t *tagIndex) add(key string, tags []string) []string {
	//: an overwrite must not leave the old tags pointing at this key.
	t.remove(key)
	//: an untagged entry costs nothing — do not create a byKey row for it.
	if len(tags) == 0 {
		//: nothing to index.
		return nil
	}
	owned := make([]string, 0, len(tags))
	//: index each tag, skipping any duplicate the caller passed.
	for _, tag := range tags {
		//: collapse duplicates: a tag is a set membership, and indexing the
		//: same pair twice would make the removed count wrong.
		if _, already := t.byTag[tag][key]; already {
			//: already indexed under this tag.
			continue
		}
		//: create the bucket on first sight of the tag.
		if t.byTag[tag] == nil {
			//: one bucket per distinct tag in the store.
			t.byTag[tag] = make(map[string]struct{}, initialIndexCapacity)
		}
		t.byTag[tag][key] = struct{}{}
		owned = append(owned, tag)
	}
	t.byKey[key] = owned
	//: the slice the store will hold alongside the entry.
	return owned
}

// remove unlinks key from every tag it carried. It is the operation that keeps
// the reverse index from growing without bound, so it MUST be called on every
// path that drops an entry: Delete, overwrite, LRU eviction and TTL expiry.
func (t *tagIndex) remove(key string) {
	//: the overwhelmingly common case is an untagged key.
	tags, found := t.byKey[key]
	if !found {
		//: nothing was indexed for this key.
		return
	}
	//: unlink this key from every bucket it appears in — O(T) in ITS OWN tags,
	//: which is exactly what byKey exists to make possible.
	for _, tag := range tags {
		bucket := t.byTag[tag]
		delete(bucket, key)
		//: drop the empty bucket so the outer map tracks LIVE tags rather than
		//: every tag the store has ever seen.
		if len(bucket) == 0 {
			//: the tag now names nothing.
			delete(t.byTag, tag)
		}
	}
	delete(t.byKey, key)
}

// keysFor returns the keys carrying tag, as a fresh slice.
//
// The copy is not defensive politeness: the caller deletes each key, which
// mutates the very bucket it would otherwise be ranging over.
func (t *tagIndex) keysFor(tag string) []string {
	bucket := t.byTag[tag]
	//: a tag that names nothing yields nothing — not an error.
	if len(bucket) == 0 {
		//: no keys carry this tag.
		return nil
	}
	//: the caller's to iterate and delete from.
	return slices.Collect(maps.Keys(bucket))
}
