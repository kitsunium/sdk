package process

import (
	"context"
)

// MustStart is like [Start] but panics with the typed error when the spawn fails
// — UnsupportedPlatform off Unix/Windows, InvalidSpec, RlimitFailed, … It is the
// idiomatic Go MustX opt-in (like [regexp.MustCompile]) for a consumer that
// chooses crash-on-failure at its own startup; the SDK itself never panics, and
// [Start] is the non-panicking form for normal use. The panic value is the typed
// error, so a top-level recover() can classify it via errs.CodeOf / HasCode.
func MustStart(ctx context.Context, spec Spec) Process {
	live, err := Start(ctx, spec)
	//: a failed spawn is the consumer's chosen crash point.
	if err != nil {
		//: panic with the typed error value, never a bare string.
		panic(err)
	}
	//: the live process handle on success.
	return live
}
