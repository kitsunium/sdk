// Package cache — the immutable value a caller hands to a Store.
package cache

import "time"

// NoExpiry is the [EntryValue.TTL] that stores an entry with no deadline.
//
// It is a distinct value from the zero TTL, which means "use the store's
// default". A cache with a one-minute default holding one entry that must
// never expire is a real combination, and reading both meanings out of 0 would
// make it unspellable — the caller would have to know the default and restate
// it, which stops being true the day the default changes.
const NoExpiry time.Duration = -1

// EntryValue is what a caller stores: the value, how long it should live, and
// the tags by which it can later be invalidated in bulk.
//
// It is a VALUE — a store copies what it needs and never retains the slice the
// caller passed, so a caller may reuse or mutate its Tags slice afterwards.
type EntryValue[V any] struct {
	// Value is the payload.
	Value V
	// TTL is how long the entry stays fetchable.
	//
	//   > 0        this exact lifetime
	//   0          the store's configured default
	//   [NoExpiry] no deadline at all
	//
	// Any other negative value is a mistake, not a third meaning, and is
	// refused with [CacheEntryRejected] rather than reinterpreted.
	TTL time.Duration
	// Tags label the entry for bulk invalidation. Order is not significant and
	// duplicates are collapsed. An empty tag is refused: it names a set nobody
	// can ask for, so accepting it would index the entry under a key that can
	// only ever be reached by accident.
	//
	// Tags cost memory in the store's reverse index — see
	// internal/service/cache/CLAUDE.md, which states the per-pair cost and
	// measures it.
	Tags []string
}
