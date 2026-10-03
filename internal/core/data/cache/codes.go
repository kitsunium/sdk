// Package cache — range 0.2.18.* (ADR 0049 core/data/cache block), and the
// chained store's 0.3.48.* (ADR 0049 service/data/cache block, declared here
// since ADR 0160).
package cache

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.18.0 - 0.2.18.255

// CodeCacheMisconfigured identifies a store refused AT CONSTRUCTION because
// its configuration cannot be honoured — a non-positive capacity, a negative
// default TTL. It is never an operational outcome: no entry was ever stored.
const CodeCacheMisconfigured errs.Code = 0x00_02_12_01 // 0.2.18.1

// CodeCacheBackendFailed identifies a store that could not serve an operation
// for a reason of its own — the transport, the medium, the process it depends
// on. A plain MISS is not this: a cache that does not hold something has done
// nothing wrong and reports (zero, false, nil).
const CodeCacheBackendFailed errs.Code = 0x00_02_12_02 // 0.2.18.2

// CodeCacheFillFailed identifies a Loader.Load whose caller-supplied fill
// returned an error. NOTHING was stored: a failed fill must not be cached, or
// one bad minute at the origin becomes a TTL's worth of bad answers.
//
// It applies only when the fill's error carries no SDK code of its own. One
// that does propagates untouched, because relabelling it would discard the
// origin's own diagnosis (ADR 0005 §origin wins).
const CodeCacheFillFailed errs.Code = 0x00_02_12_03 // 0.2.18.3

// CodeCacheEntryRejected identifies an entry a store refuses to store: an
// empty key, an empty tag, or a negative TTL that is not cache.NoExpiry.
//
// Each of the three is a value that has no meaning rather than an unusual one,
// and storing it would produce an entry the caller can never deliberately
// fetch, invalidate or replace.
const CodeCacheEntryRejected errs.Code = 0x00_02_12_04 // 0.2.18.4

// range: 0.3.48.0 - 0.3.48.255 — the chained store's own codes, allocated by
// the service layer (LL = 3) and declared here since ADR 0160: a code keeps the
// value its layer allocated when its declaration moves.

// CodeCacheChainMisconfigured identifies a NewChain refused AT CONSTRUCTION:
// fewer than two tiers, a nil tier, or a tier that cannot answer the whole
// contract a tier must answer (see the errors.go sentinel for why that is
// checked once, up front, rather than discovered per call).
const CodeCacheChainMisconfigured errs.Code = 0x00_03_30_01 // 0.3.48.1

// CodeCacheTierFailed identifies a chained operation that failed inside one
// specific tier.
//
// It is distinct from CACHE_BACKEND_FAILED because a chain has more than one
// backend, and "the cache is broken" and "the far tier is broken" are
// different incidents with different responses: the first is an outage, the
// second is a degradation the near tier is still absorbing.
const CodeCacheTierFailed errs.Code = 0x00_03_30_02 // 0.3.48.2
