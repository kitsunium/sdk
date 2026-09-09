// Package cache — the sibling interfaces a store advertises by type assertion
// (ADR 0039), kept out of cache.go so the FROZEN port stands alone in the file
// that names the package.
package cache

import "context"

// EntryFetcher is the sibling that returns the whole entry rather than only
// its value (ADR 0039). It exists because promotion between tiers has to carry
// TTL and tags: a value promoted into a near tier without its tags is a value
// that [Tagger.InvalidateTag] can no longer reach, and an invalidation that
// silently misses one copy is worse than one that is not offered at all.
type EntryFetcher[V any] interface {
	// FetchEntry is [Store.Fetch] returning the stored entry — value, remaining
	// TTL and tags. It mutates the store for the same reasons Fetch does.
	//
	// The TTL it reports is the REMAINING time, not the one originally
	// supplied: promoting an entry with its original TTL would extend its life
	// every time it moved between tiers.
	FetchEntry(ctx context.Context, key string) (EntryValue[V], bool, error)
}

// Tagger is the sibling for stores that index entries by tag (ADR 0039).
//
// Tagging exists because the thing a caller wants to invalidate is usually not
// a key. "Every entry derived from user 42" is a set whose members are known
// to the writer and not to the invalidator, and without a tag the invalidator's
// only options are to guess the key shape or to purge everything.
type Tagger interface {
	// InvalidateTag removes every entry carrying tag and reports how many it
	// removed. A tag that names nothing removes zero and reports nil.
	//
	// The cost is proportional to the number of entries carrying the tag, NOT
	// to the size of the store — see internal/service/cache/BENCH.md, which
	// measures it at two store sizes for exactly this claim.
	InvalidateTag(ctx context.Context, tag string) (removed int, err error)
}

// Fill computes the entry for a key that the cache does not hold. It is the
// caller's code: a database query, an HTTP call, a derivation.
//
// It is a FUNC port rather than a one-method interface, the shape
// internal/core already admits for resilience.Operation, scheduler.Job and
// validation.Constraint — and, per ADR 0039, a func type satisfies the
// no-widening rule structurally, because it cannot grow a method at all.
type Fill[V any] func(ctx context.Context) (EntryValue[V], error)

// Loader is the sibling for stores that can fill a miss while ensuring that
// concurrent misses on ONE key run the fill ONCE (ADR 0039).
//
// # What it protects against, and what it does not
//
// A cache stampede is what happens when a hot key expires: every request that
// was being served from it misses at the same instant and every one of them
// calls the origin. The origin sees, in one moment, the traffic the cache was
// hiding — which is the load the cache was bought to prevent, arriving when
// the system is least able to absorb it.
//
// [Loader.Load] collapses those calls into one WITHIN ONE PROCESS. It does
// NOT coordinate across replicas: N instances of a service each running this
// still produce N concurrent fills. Cross-process collapsing needs a shared
// lease, which is a distributed system and not something a cache port can
// promise. A reader who takes "stampede protection" to mean "my origin sees
// one request" would be wrong by a factor of N, so the limit is stated here,
// in the port, and again in every document that mentions the feature.
type Loader[V any] interface {
	// Load returns the value for key, calling fill exactly once across all
	// concurrent callers of this key in this process if the cache does not
	// hold it. A fill that fails stores nothing and returns
	// [CacheFillFailed] — unless the failure already carries an SDK code, in
	// which case it propagates untouched (ADR 0005 §origin wins).
	//
	// fill does NOT receive ctx. It receives the shared call's context: the
	// first caller's values with every deadline removed, cancelled only once
	// every waiting caller has gone. A caller whose own ctx ends while it
	// waits gets its own ctx.Err() and leaves the fill running for the others.
	Load(ctx context.Context, key string, fill Fill[V]) (V, error)
}
