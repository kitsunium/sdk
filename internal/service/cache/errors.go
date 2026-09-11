// Package cache — declares the sentinel *errs.Error values the chained store
// emits. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form.
package cache

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A chain refused at construction
// is a permanent configuration fault: the same tiers will be refused forever,
// and the fix is a code change at the call site, never a retry.
const exitChainConfig int = 78

var (
	// CacheChainMisconfigured is returned by NewChain for a tier set it cannot
	// honour.
	//
	// The interesting refusal is the third one. Every tier must implement
	// core/cache.EntryFetcher and core/cache.Tagger, and that is checked ONCE,
	// at construction, rather than per call. The reason is the whole point of
	// tagging: promoting a value into a nearer tier WITHOUT its tags produces
	// a copy that InvalidateTag can no longer reach, so the invalidation
	// reports success, the far tier forgets the entry, and the near tier keeps
	// serving it until its TTL runs out. A tag invalidation that silently
	// misses one copy is worse than one that was never offered — so the chain
	// refuses to be built rather than degrade at the moment nobody is looking.
	CacheChainMisconfigured = errs.Define(CodeCacheChainMisconfigured, "CACHE_CHAIN_MISCONFIGURED",
		"The cache chain cannot be built from the given tiers",
		"service/cache: NewChain received fewer than two tiers, a nil tier, or a tier that does not implement EntryFetcher and Tagger; the fields name the position and the missing capability",
		errs.WithExitCode(exitChainConfig))

	// CacheTierFailed is returned when a chained operation failed inside one
	// tier. Its fields name the tier's position and the operation, because a
	// chain's caller holds one Store and would otherwise have no way to tell
	// which of two backends broke.
	CacheTierFailed = errs.Define(CodeCacheTierFailed, "CACHE_TIER_FAILED",
		"A tier of the cache chain could not serve the operation",
		"service/cache: one tier of a chained store returned an error; the fields name the tier position, the operation and the key")
)
