// Package cache — the refusals every store shares, and the wrapping rule for
// a caller's fill error.
package cache

import (
	"slices"
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/cache"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// validateEntry rejects an entry no store can represent and returns the TTL to
// hand the underlying primitive (0 = no deadline).
//
// The three refusals are each the ADR 0031 "refuse" half applied to an
// OPERATION rather than to a constructor: none of them has a sensible reading,
// and every one of them would produce an entry the caller can never
// deliberately reach again.
//
// A key or a tag reaching an error FIELD reaches the operator's LOG. Fields
// are log-only and never rendered by Error() (ADR 0005 §4), but a caller who
// puts a secret in a cache key has put that secret in their logs, and this is
// the place that says so.
func validateEntry[V any](key string, entry corecache.EntryValue[V]) error {
	//: an empty key is fetchable only by another empty key, and it collides
	//: with every other caller who made the same mistake.
	if key == "" {
		//: refuse rather than store an unreachable entry.
		return rejected("key", "empty", "")
	}
	//: an empty tag names a set nobody can ask for, so the entry would be
	//: indexed under a bucket only another accident could reach.
	if slices.Contains(entry.Tags, "") {
		//: refuse rather than index an unaskable set.
		return rejected("tag", "empty", key)
	}
	//: NoExpiry is the ONE negative TTL with a meaning; every other negative
	//: value would be read by the primitive as "no deadline", so accepting it
	//: silently stores an entry that never goes away.
	if entry.TTL < 0 && entry.TTL != corecache.NoExpiry {
		//: refuse rather than reinterpret.
		return rejected("ttl", entry.TTL.String(), key)
	}
	//: storable as given.
	return nil
}

// primitiveTTL maps the domain's three TTL meanings onto the kernel
// primitive's two ("0 = forever", "positive = this long"). storeDefault is the
// store's own DefaultTTL, itself already validated as non-negative.
func primitiveTTL(ttl, storeDefault time.Duration) time.Duration {
	//: three domain meanings, collapsed onto the two the primitive has.
	switch ttl {
	//: an explicit "no deadline".
	case corecache.NoExpiry:
		//: which is what the primitive spells 0.
		return 0
	//: the unstated lifetime.
	case 0:
		//: defer to the store — which may itself be 0, in which case the entry
		//: has no deadline and the two spellings converge.
		return storeDefault
	//: an explicit positive lifetime.
	default:
		//: as given.
		return ttl
	}
}

// rejected builds the CACHE_ENTRY_REJECTED refusal, naming the offending part.
func rejected(part, problem, key string) error {
	fields := []kerrs.FieldValue{
		kerrs.String("part", part),
		kerrs.String("problem", problem),
	}
	//: the key is omitted when it is the thing being refused — there is
	//: nothing to name.
	if key != "" {
		//: log-only; see validateEntry's note about keys in logs.
		fields = append(fields, kerrs.String("key", key))
	}
	//: origin-wins keeps the sentinel's code and public message.
	return kerrs.Wrap(corecache.CacheEntryRejected, kerrs.WrapParams{}, fields...)
}

// fillFailure labels a caller's failed fill — unless it already carries an SDK
// code, in which case it propagates untouched.
//
// Relabelling a typed error would discard the origin's own diagnosis and
// replace it with the generic one, which is precisely the trade ADR 0005
// §"origin wins" refuses. It is the same normalisation config.Load performs at
// its Source boundary, and for the same reason: Fill is caller code, so the
// error can be anything.
//
// An untyped cause is labelled AND kept. It used to survive only as a string
// field, so a fill returning sql.ErrNoRows came back as an error for which
// errors.Is(err, sql.ErrNoRows) was false — the caller's own "not found" was
// indistinguishable from an outage. Wrapping the cause itself keeps both
// answers: errs.HasCode sees CACHE_FILL_FAILED, errors.Is sees the cause.
func fillFailure(key string, cause error) error {
	//: a cause already carrying a dotted quad keeps it, trail and all.
	if _, typed := kerrs.CodeOf(cause); typed {
		//: the origin's own diagnosis.
		return cause
	}
	//: label the untyped failure so Load's contract holds whoever wrote fill,
	//: with the cause IN the chain. The identity is read from the sentinel so
	//: it cannot drift from it; the cause field keeps the log line it always
	//: had.
	return kerrs.Wrap(cause, kerrs.WrapParams{
		Code:    corecache.CacheFillFailed.Code(),
		Reason:  corecache.CacheFillFailed.Reason(),
		Public:  corecache.CacheFillFailed.Public(),
		Private: corecache.CacheFillFailed.Private(),
	}, kerrs.String("key", key), kerrs.String("cause", cause.Error()))
}
