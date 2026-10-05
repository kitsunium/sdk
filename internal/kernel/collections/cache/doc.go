// Package cache provides a generic, concurrency-safe LRU + TTL cache
// (Cache[K,V]) — a kernel primitive (stdlib-only, no domain vocabulary). It
// reuses the kernel clock so TTL expiry is testable. Fetch on a hit is
// allocation-free (map lookup + intrusive-list splice); Set allocates one entry
// per new key. Reads and writes are serialised by a single mutex. Fetch is
// named Fetch (not Get) because a hit mutates state (LRU promotion + counters).
//
// Package cache — the New constructor configuration.
//
// Package cache — the intrusive doubly-linked LRU node.
//
// Package cache — the key/value pair carried out of the locked section so the
// OnEvict observer never runs while the cache mutex is held.
//
// Package cache — the read-only counters snapshot.
package cache
