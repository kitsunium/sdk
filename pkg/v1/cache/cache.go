//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/cache .

// Package cache is the public facade for the SDK's generic LRU + TTL cache.
// It re-exports the kernel primitive as type aliases (zero runtime cost) so
// consumers depend only on pkg/v1.
//
//	c := cache.New[string, int](cache.Config[string, int]{MaxEntries: 1024, DefaultTTL: time.Minute})
//	c.Set("answer", 42)
//	v, ok := c.Fetch("answer") // 42, true
//
// The read verb is [Cache.Fetch], not Get: a hit mutates state (it promotes the
// entry to most-recently-used and bumps the hit counter), so naming it Get
// would promise a pure read the cache does not offer.
//
// The cache is safe for concurrent use; Fetch on a hit is allocation-free. An
// OnEvict observer runs AFTER the internal lock is released, so it may re-enter
// the cache without deadlocking.
package cache

import kcache "github.com/kitsunium/sdk/internal/kernel/cache"

// Cache is the public alias for the generic LRU+TTL cache.
type Cache[K comparable, V any] = kcache.Cache[K, V]

// Config is the public alias for the cache constructor configuration.
type Config[K comparable, V any] = kcache.Config[K, V]

// Stats is the public alias for the cache counters snapshot.
type Stats = kcache.StatsValue

// New builds a Cache from cfg. A nil cfg.Clock defaults to the system clock.
func New[K comparable, V any](cfg Config[K, V]) *Cache[K, V] {
	//: delegate to the kernel constructor.
	return kcache.NewCache(cfg)
}
