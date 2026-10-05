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
// [Loader].Load guarantees ONE fill per key across every goroutine in THIS
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
// implying a consistency it cannot deliver. See internal/service/data/cache's
// CLAUDE.md for each choice and its reason.
package cache
