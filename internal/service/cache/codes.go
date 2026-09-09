// Package cache — range 0.3.48.* (ADR 0049 service/cache block).
package cache

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.48.0 - 0.3.48.255

// CodeCacheChainMisconfigured identifies a NewChain refused AT CONSTRUCTION:
// fewer than two tiers, a nil tier, or a tier that cannot answer the whole
// contract a tier must answer (see the errors.go sentinel for why that is
// checked once, up front, rather than discovered per call).
const CodeCacheChainMisconfigured errs.Code = 0x00_03_30_01 // 0.3.48.1

// CodeCacheTierFailed identifies a chained operation that failed inside one
// specific tier.
//
// It is distinct from core's CACHE_BACKEND_FAILED because a chain has more
// than one backend, and "the cache is broken" and "the far tier is broken" are
// different incidents with different responses: the first is an outage, the
// second is a degradation the near tier is still absorbing.
const CodeCacheTierFailed errs.Code = 0x00_03_30_02 // 0.3.48.2
