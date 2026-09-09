// Package cache — declares the sentinel *errs.Error port outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
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
		"core/cache: a store constructor received a capacity or a default TTL it cannot honour; the fields name the option and the value",
		errs.WithExitCode(exitConfig))

	// CacheBackendFailed is returned when the store itself could not serve the
	// operation. A miss is NOT this — a miss is (zero, false, nil).
	CacheBackendFailed = errs.Define(CodeCacheBackendFailed, "CACHE_BACKEND_FAILED",
		"The cache backend could not serve the operation",
		"core/cache: a store failed for a reason of its own rather than reporting a miss; the fields name the operation and the key")

	// CacheFillFailed is returned by Loader.Load when the caller's fill fails
	// and its error carries no SDK code of its own.
	//
	// Nothing is stored. Caching a failure would turn one bad minute at the
	// origin into a full TTL of bad answers, served confidently, to every
	// caller — the failure mode a cache is least able to explain afterwards.
	CacheFillFailed = errs.Define(CodeCacheFillFailed, "CACHE_FILL_FAILED",
		"The cache fill function failed and nothing was stored",
		"core/cache: a Loader.Load fill returned an error carrying no SDK code; the fields name the key")

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
		"core/cache: Set received an empty key, an empty tag, or a negative TTL that is not cache.NoExpiry; the fields name the offending part",
		errs.WithExitCode(exitDataErr))
)
