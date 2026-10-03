// Package cache — declares the sentinel *errs.Error port outcomes, and the
// two the chained store in internal/service/data/cache emits (ADR 0160: every
// code is declared in the core, at the service's path). Each var's name equals
// its errs.Define Reason in SCREAMING_SNAKE form.
package cache

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A store refused at construction
// is a permanent configuration fault: the same arguments will be refused
// forever, and the fix is a code change at the call site, never a retry.
const exitConfig int = 78

// exitDataErr matches sysexits EX_DATAERR (65). A rejected entry is malformed
// input to the store, not a broken store and not a broken configuration.
const exitDataErr int = 65

var (
	// CacheMisconfigured is returned by a store CONSTRUCTOR whose
	// configuration it cannot honour.
	//
	// It is ADR 0031's refuse half. A capacity is the caller's intent and any
	// number the SDK picked would be arbitrary, so a non-positive one is
	// refused rather than clamped — and refusing at construction is what keeps
	// the other outcome off the table: a cache built with an unusable
	// configuration still answers every call correctly, it just never HOLDS
	// anything, and nothing downstream can tell the difference between that
	// and a genuinely cold workload.
	CacheMisconfigured = errs.Define(CodeCacheMisconfigured, "CACHE_MISCONFIGURED",
		"The cache cannot be built from the given configuration",
		"core/data/cache: a store constructor received a capacity or a default TTL it cannot honour; the fields name the option and the value",
		errs.WithExitCode(exitConfig))

	// CacheBackendFailed is returned when the store itself could not serve the
	// operation. A miss is NOT this — a miss is (zero, false, nil).
	CacheBackendFailed = errs.Define(CodeCacheBackendFailed, "CACHE_BACKEND_FAILED",
		"The cache backend could not serve the operation",
		"core/data/cache: a store failed for a reason of its own rather than reporting a miss; the fields name the operation and the key")

	// CacheFillFailed is returned by Loader.Load when the caller's fill fails
	// and its error carries no SDK code of its own.
	//
	// Nothing is stored. Caching a failure would turn one bad minute at the
	// origin into a full TTL of bad answers, served confidently, to every
	// caller — the failure mode a cache is least able to explain afterwards.
	CacheFillFailed = errs.Define(CodeCacheFillFailed, "CACHE_FILL_FAILED",
		"The cache fill function failed and nothing was stored",
		"core/data/cache: a Loader.Load fill returned an error carrying no SDK code; the fields name the key")

	// CacheEntryRejected is returned by Set for an entry the store refuses:
	// an empty key, an empty tag, or a negative TTL other than NoExpiry.
	//
	// Refusing is the point. An empty key is fetchable only by another empty
	// key, an empty tag names a set nobody can ask for, and a negative TTL
	// would be read by the underlying primitive as "no expiry" — so the three
	// silent outcomes of accepting them are an entry nobody meant to write, an
	// entry nobody can invalidate, and an entry that never goes away.
	CacheEntryRejected = errs.Define(CodeCacheEntryRejected, "CACHE_ENTRY_REJECTED",
		"The cache entry is not storable as given",
		"core/data/cache: Set received an empty key, an empty tag, or a negative TTL that is not cache.NoExpiry; the fields name the offending part",
		errs.WithExitCode(exitDataErr))

	// CacheChainMisconfigured is returned by NewChain for a tier set it cannot
	// honour.
	//
	// The interesting refusal is the third one. Every tier must implement
	// [EntryFetcher] and [Tagger], and that is checked ONCE, at construction,
	// rather than per call. The reason is the whole point of tagging:
	// promoting a value into a nearer tier WITHOUT its tags produces a copy
	// that InvalidateTag can no longer reach, so the invalidation reports
	// success, the far tier forgets the entry, and the near tier keeps
	// serving it until its TTL runs out. A tag invalidation that silently
	// misses one copy is worse than one that was never offered — so the chain
	// refuses to be built rather than degrade at the moment nobody is looking.
	CacheChainMisconfigured = errs.Define(CodeCacheChainMisconfigured, "CACHE_CHAIN_MISCONFIGURED",
		"The cache chain cannot be built from the given tiers",
		"service/data/cache: NewChain received fewer than two tiers, a nil tier, or a tier that does not implement EntryFetcher and Tagger; the fields name the position and the missing capability",
		errs.WithExitCode(exitConfig))

	// CacheTierFailed is returned when a chained operation failed inside one
	// tier. Its fields name the tier's position and the operation, because a
	// chain's caller holds one Store and would otherwise have no way to tell
	// which of two backends broke.
	CacheTierFailed = errs.Define(CodeCacheTierFailed, "CACHE_TIER_FAILED",
		"A tier of the cache chain could not serve the operation",
		"service/data/cache: one tier of a chained store returned an error; the fields name the tier position, the operation and the key")
)
