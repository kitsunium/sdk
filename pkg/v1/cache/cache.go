//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/cache .

// Package cache is the public facade for the SDK's caching, and it offers two
// surfaces because there are two different jobs.
//
// # The primitive: Cache[K,V]
//
// [Cache] is a generic LRU + TTL map — construct it, Set, Fetch, done. No
// port, no error surface, no vocabulary. Reach for it when you want a bounded
// map with expiry inside one component.
//
//	c := cache.New[string, int](cache.Config[string, int]{MaxEntries: 1024, DefaultTTL: time.Minute})
//	c.Set("answer", 42)
//	v, ok := c.Fetch("answer") // 42, true
//
// # The domain: Store[V]
//
// [Store] is the cache as an INTERFACE, with the three things a primitive
// deliberately does not have: invalidation by tag, protection against a
// stampede of concurrent misses, and the ability to put one store in front of
// another. Reach for it when a cache is part of your architecture rather than
// an implementation detail — when something else has to be able to invalidate
// what you cached, or when the thing behind the cache must not be hit N times
// the instant a hot key expires.
//
//	store, err := cache.NewMemory[Profile](cache.MemoryConfig{
//	    MaxEntries: 10_000,       // refused if zero — a capacity is your decision
//	    DefaultTTL: time.Minute,
//	})
//
//	loader, _ := store.(cache.Loader[Profile])
//	profile, err := loader.Load(ctx, key, func(ctx context.Context) (cache.Entry[Profile], error) {
//	    p, err := db.Profile(ctx, id)
//	    return cache.Entry[Profile]{Value: p, TTL: time.Minute, Tags: []string{"user:" + id}}, err
//	})
//
//	tagger, _ := store.(cache.Tagger)
//	removed, err := tagger.InvalidateTag(ctx, "user:"+id) // every entry derived from that user
//
// The two do not compete: the domain store is BUILT on the primitive, and
// [Cache] stays exactly what it was.
//
// # Fetch, not Get — on both surfaces
//
// A hit promotes the entry in the recency order and moves the counters, so a
// read MUTATES. Naming it Get would promise a purity neither surface offers,
// and in the domain store it takes a write lock for precisely that reason.
//
// # Stampede protection stops at the process boundary
//
// [Loader.Load] guarantees ONE fill per key across every goroutine in THIS
// process. It does not coordinate across replicas. Ten instances of a service
// each running this still send ten concurrent requests to the origin when a
// hot key expires — so a fleet's worst-case origin load falls by the
// concurrency factor within an instance, not to one. Cross-process collapsing
// needs a shared lease, which is a distributed system and not something a
// cache can promise. Size the origin against the number of replicas, not
// against the number of requests.
//
// # Nothing across tiers is atomic
//
// [NewChain] is two independent stores, and there is no transaction between
// them. Every multi-tier operation has a window in which they disagree; the
// package chooses which side of that window is safer and says so, rather than
// implying a consistency it cannot deliver. See internal/service/cache's
// CLAUDE.md for each choice and its reason.
package cache

import (
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/cache"
	kcache "github.com/kitsunium/sdk/internal/kernel/cache"
	svccache "github.com/kitsunium/sdk/internal/service/cache"
)

// NoExpiry is the [Entry].TTL that stores an entry with no deadline.
//
// It is deliberately distinct from the zero TTL, which means "use the store's
// default". A store with a one-minute default holding one entry that must
// never expire is a real combination, and reading both meanings out of 0 would
// make the second one unsayable.
const NoExpiry time.Duration = corecache.NoExpiry

// Cache is the public alias for the generic LRU+TTL primitive.
type Cache[K comparable, V any] = kcache.Cache[K, V]

// Config is the public alias for the primitive's constructor configuration.
type Config[K comparable, V any] = kcache.Config[K, V]

// Stats is the public alias for the primitive's counters snapshot.
type Stats = kcache.StatsValue

// New builds a [Cache] from cfg. A nil cfg.Clock defaults to the system clock.
func New[K comparable, V any](cfg Config[K, V]) *Cache[K, V] {
	//: delegate to the kernel constructor.
	return kcache.NewCache(cfg)
}

// Entry is what a caller stores: the value, its lifetime, and the tags by
// which it can later be invalidated in bulk.
type Entry[V any] = corecache.EntryValue[V]

// Store is the cache port: Fetch, Set, Delete.
//
// The method set is FROZEN at three. New capabilities arrive as SIBLING
// interfaces you reach by type assertion — [EntryFetcher], [Tagger] and
// [Loader] — because this alias publishes the interface, Go interfaces are
// structural, and a fourth method would break every downstream implementer at
// compile time with no deprecation window.
type Store[V any] = corecache.Store[V]

// EntryFetcher is the sibling that returns the whole entry — value, REMAINING
// TTL, tags — rather than only the value. Both stores built here implement it.
type EntryFetcher[V any] = corecache.EntryFetcher[V]

// Tagger is the sibling for invalidating every entry carrying a tag.
//
// The cost is proportional to the entries carrying the tag, not to the size of
// the store — measured in internal/service/cache/BENCH.md at two store sizes.
type Tagger = corecache.Tagger

// Fill computes the entry for a key the cache does not hold: a query, a
// request, a derivation. It is your code.
type Fill[V any] = corecache.Fill[V]

// Loader is the sibling that fills a miss while collapsing concurrent misses
// on one key into a single fill — within one process, and not across replicas.
// See the package documentation before sizing an origin against it.
type Loader[V any] = corecache.Loader[V]

// MemoryConfig parameterises [NewMemory]. Its zero value is NOT a working
// cache — MaxEntries must be stated, and is refused rather than defaulted.
type MemoryConfig = svccache.MemoryConfig

// ChainConfig parameterises [NewChain].
type ChainConfig = svccache.ChainConfig

// NewMemory builds an in-process store: an LRU with TTL, a tag index, and
// stampede protection on Load.
//
// The returned [Store] also implements [EntryFetcher], [Tagger] and [Loader].
//
// A non-positive MaxEntries is REFUSED, not defaulted. A capacity is the whole
// statement of how much memory you are willing to spend, so any number chosen
// for you would be arbitrary — and the value a zero would naturally mean,
// unbounded, is the dangerous one.
func NewMemory[V any](cfg MemoryConfig) (store Store[V], err error) {
	//: delegate to the service constructor.
	return svccache.NewMemory[V](cfg)
}

// NewChain puts each store in front of the next: tiers[0] is consulted first,
// the last is the authority. A hit in a far tier is promoted into every nearer
// one, carrying its REMAINING TTL and its tags.
//
// Every tier must implement [EntryFetcher] and [Tagger], checked here and
// refused rather than discovered per call: a value promoted without its tags
// is one [Tagger] can no longer reach, so an invalidation would report success
// while a nearer tier kept serving the entry.
func NewChain[V any](cfg ChainConfig, tiers ...Store[V]) (store Store[V], err error) {
	//: delegate to the service constructor.
	return svccache.NewChain(cfg, tiers...)
}

var (
	// Misconfigured is returned by a store constructor whose configuration it
	// cannot honour — a non-positive capacity, a negative default TTL.
	Misconfigured = corecache.CacheMisconfigured
	// BackendFailed is returned when a store could not serve an operation. A
	// MISS is not this: a miss is (zero, false, nil).
	BackendFailed = corecache.CacheBackendFailed
	// FillFailed is returned by Load when your fill fails and its error
	// carries no SDK code of its own. Nothing is stored: caching a failure
	// turns one bad minute at the origin into a full TTL of confident wrong
	// answers.
	FillFailed = corecache.CacheFillFailed
	// EntryRejected is returned by Set for an entry no store can represent —
	// an empty key, an empty tag, or a negative TTL other than [NoExpiry].
	EntryRejected = corecache.CacheEntryRejected
	// ChainMisconfigured is returned by [NewChain] for a tier set it cannot
	// honour.
	ChainMisconfigured = svccache.CacheChainMisconfigured
	// TierFailed is returned when a chained operation failed inside one tier.
	// Its fields name the position, because a chain's caller holds one Store
	// and would otherwise have no way to tell which backend broke.
	TierFailed = svccache.CacheTierFailed
)
