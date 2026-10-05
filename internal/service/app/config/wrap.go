package config

import (
	"errors"

	coreconfig "github.com/kitsunium/sdk/internal/core/app/config"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// wrapAs returns the given config sentinel as the error origin — its code,
// reason, public message and statuses win, so an *errs.Error cause from a
// codec or a source someone else wrote cannot hijack the code — with the
// cause kept IN THE CHAIN beside it and its message in the `cause` field. A
// nil cause yields the bare sentinel.
//
// In the chain is the point. errs.Wrap of a typed cause is origin-wins and
// would hand the caller the codec's code; a cause flattened into a field
// alone loses its identity, so errs.HasCode on a codec's refusal code — or
// errors.Is against fs.ErrNotExist — stopped answering through config. The
// sentinel and the cause are joined, and the join wrapped: Wrap finds the
// sentinel first, so it is the origin and Error() renders it alone, while
// errors.Is and errs.HasCode walk the join and reach the cause.
func wrapAs(sentinel *kerrs.Error, cause error) error {
	//: a nil cause needs no field — return the sentinel as-is.
	if cause == nil {
		//: the bare typed sentinel.
		return sentinel
	}
	//: the sentinel is the origin, the cause rides beside it in the chain,
	//: and its message in the same field a stated refusal uses.
	return kerrs.Wrap(errors.Join(sentinel, cause), kerrs.WrapParams{}, kerrs.String("cause", cause.Error()))
}

// withCause returns the given config sentinel as the error origin with cause as
// its `cause` field — wrapAs for a refusal this package states itself. An input
// guard has a sentence to say and no error to wrap, and minting a stdlib error
// only to have its Error() copied into the field would be the untyped error
// rule 2 bans.
func withCause(sentinel *kerrs.Error, cause string) error {
	//: wrap the sentinel (origin-wins keeps its code) + carry the cause text.
	return kerrs.Wrap(sentinel, kerrs.WrapParams{}, kerrs.String("cause", cause))
}

// keep the core import referenced even if a source file is built alone.
var _ = coreconfig.ConfigSourceFailed
