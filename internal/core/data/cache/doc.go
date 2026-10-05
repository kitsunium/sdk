// Package cache declares the SDK's cache DOMAIN: the [Store] port that owns a
// set of keyed entries, the [EntryValue] a caller hands it, and the three
// sibling interfaces through which a store advertises what else it can do.
// A core sibling admitted by ADR 0049, which amends ADR 0025.
//
// # This is not internal/kernel/collections/cache
//
// [github.com/kitsunium/sdk/internal/kernel/collections/cache] is a PRIMITIVE: a generic
// LRU + TTL map with no port, no error surface and no vocabulary. It stays
// exactly what it is, and the concrete store in internal/service/data/cache is
// built on top of it.
//
// This package is the DOMAIN: an interface a caller can hold without naming an
// implementation, plus the three things a primitive deliberately does not
// have — invalidation by tag, protection against a stampede of concurrent
// misses, and the ability to put one store in front of another.
//
// # A read is not free, and the port says so
//
// The read verb is [Store.Fetch], not Get, and the name is load-bearing. A hit
// promotes the entry in the store's recency order and moves its counters; in
// the concrete memory store it takes a WRITE lock for exactly that reason. Two
// concurrent Fetches of one key are therefore not interchangeable with one,
// and a Fetch is not something to sprinkle through a function that must not
// mutate. ADR 0025 named the primitive's method Fetch for this reason; the
// port keeps the name so the honesty survives the layer boundary.
//
// # There is no registry
//
// Like proc (ADR 0016), resilience (ADR 0026), net (ADR 0029), scheduler
// (ADR 0041), token (ADR 0042), session (ADR 0045) and validation (ADR 0046),
// this domain has no name-to-implementation registry.
//
// The domain reason is session's: the set of backends is closed and small, and
// picking one is a deployment decision made in code. Resolving it from a
// configuration string would let a typo silently downgrade a shared cache to a
// per-process one — and unlike a downgraded logger, a downgraded cache still
// answers every call correctly, so nothing would ever surface.
//
// The mechanical reason is decisive on its own: [Store] is generic in V, and
// Go has no map from a name to Store[V] for an open V. A registry here would
// have to erase the type and hand it back as any, which is precisely the
// unchecked cast the typed port exists to remove.
//
// # Keys are strings, values are not
//
// [Store] fixes the key type to string while leaving V generic. The primitive
// is generic in both, and the domain deliberately narrows one half: a domain
// key appears in a tag bucket, in an error field and in a log line, and a
// generic K would need a formatting rule for each of those before it could.
// A caller with a non-string key formats it once, at the edge, where the
// encoding is a decision rather than a default.
//
// Package cache — the sibling interfaces a store advertises by type assertion
// (ADR 0039), kept out of cache.go so the FROZEN port stands alone in the file
// that names the package.
//
// Package cache — range 0.2.18.* (ADR 0049 core/data/cache block), and the
// chained store's 0.3.48.* (ADR 0049 service/data/cache block, declared here
// since ADR 0160).
//
// Package cache — the immutable value a caller hands to a Store.
//
// Package cache — declares the sentinel *errs.Error port outcomes, and the
// two the chained store in internal/service/data/cache emits (ADR 0160: every
// code is declared in the core, at the service's path). Each var's name equals
// its errs.Define Reason in SCREAMING_SNAKE form.
package cache
