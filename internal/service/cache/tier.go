// Package cache — one level of a chained store.
package cache

import corecache "github.com/kitsunium/sdk/internal/core/cache"

// tier is one level of a chain, resolved to the three interfaces a tier must
// answer. Resolving them ONCE at construction is what keeps promotion faithful
// (see [CacheChainMisconfigured]) and keeps the read path free of repeated type
// assertions.
type tier[V any] struct {
	store corecache.Store[V]
	entry corecache.EntryFetcher[V]
	tags  corecache.Tagger
}
