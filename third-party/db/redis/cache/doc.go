// Package cache is the remote tier of the cache domain: a core/data/cache
// Store over Valkey or Redis through go-redis, implementing the four ports —
// Store, EntryFetcher, Tagger and Loader — so internal/service/data/cache's
// NewChain accepts it as its far tier behind a memory store (ADR 0049,
// ADR 0211). It lives in the go-redis vendor module, third-party/db/redis,
// named by its vendor rather than by one package (#984).
//
// # Keys
//
// Every key of one store shares the hash tag of Config.Prefix, so the store
// lives on one slot of a cluster and every write is one atomic script: the
// value under {prefix}:v:<key>, the entry's tags under {prefix}:t:<key>, and
// each tag's members under {prefix}:g:<tag>. A store therefore does not shard
// across a cluster; two stores with two prefixes do.
//
// # What the server owns
//
// An entry's deadline is the server's: Set writes it with PX, FetchEntry reads
// the remaining TTL with PTTL, so promotion carries the time left, never the
// time first asked for. Values are encoded with Config.Codec; a value that
// does not decode is ValueUndecodable, never a silent miss.
//
// # Loader across replicas
//
// Load collapses concurrent misses on one key within the process, as the
// port promises, and, when Config.FillLease is positive, across processes:
// the first caller takes {prefix}:l:<key> with SET NX PX FillLease and fills,
// the others poll every FillPoll on the injected clock until the value
// appears or the lease is gone. The lease is not fenced — a filler that
// outlives it may race a second one, and the later Set wins — the
// non-guarantee the lock domain states for its own leases (ADR 0052).
package cache
