// Package cache — the key/value pair carried out of the locked section so the
// OnEvict observer never runs while the cache mutex is held.
package cache

// evictionValue is one evicted key/value pair, captured BY VALUE at eviction
// time. Copying is deliberate: the *entry it came from is unlinked and may be
// reused or collected before the observer runs, so holding the pointer would
// hand the callback a node the cache no longer owns.
type evictionValue[K comparable, V any] struct {
	key K
	val V
}
