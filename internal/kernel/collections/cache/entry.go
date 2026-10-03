// Package cache — the intrusive doubly-linked LRU node.
package cache

import "time"

// entry is one cache slot: it holds the key (so eviction can delete it from the
// map), the value, an optional absolute expiry, and the prev/next links of the
// intrusive LRU list (head = most-recently-used, tail = least).
type entry[K comparable, V any] struct {
	key      K
	val      V
	expireAt time.Time // zero = no expiry
	prev     *entry[K, V]
	next     *entry[K, V]
}
