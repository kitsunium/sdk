package cache

import (
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
)

// commandFailed types a failed command: COMMAND_FAILED, the domain's
// CACHE_BACKEND_FAILED in its trail, go-redis's error in the chain. The key
// is never attached: a cache key may carry a caller's identifier.
func commandFailed(cause error, op string) error {
	coded := errs.Wrap(cause, CommandFailed.Params(), errs.String("op", op))
	//: origin wins: the package's code stays, the domain's joins the trail.
	return errs.Wrap(coded, corecache.CacheBackendFailed.Params())
}

// undecodable reports a stored value the codec cannot decode.
func undecodable(cause error, codecName string) error {
	//: the codec's error kept, the bytes never echoed.
	return errs.Wrap(cause, ValueUndecodable.Params(), errs.String("codec", codecName))
}

// rejected refuses an entry, naming the part at fault, as the memory store
// refuses it.
func rejected(part, problem string) error {
	//: origin wins: the domain's sentinel.
	return errs.Wrap(corecache.CacheEntryRejected, errs.WrapParams{},
		errs.String("part", part), errs.String("problem", problem))
}

// fillFailure labels a caller's failed fill CACHE_FILL_FAILED, unless it
// already carries an SDK code, which then propagates untouched (ADR 0005).
func fillFailure(cause error) error {
	//: the origin's own diagnosis.
	if _, typed := errs.CodeOf(cause); typed {
		return cause
	}
	//: labelled, the cause kept in the chain.
	return errs.Wrap(cause, corecache.CacheFillFailed.Params())
}
