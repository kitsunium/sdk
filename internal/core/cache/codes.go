// Package cache — range 0.2.18.* (ADR 0049 core/cache block).
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
