// Package config provides concrete configuration sources (env, file), the
// merge+decode loader, and a cross-OS poll watcher implementing core/config.
// Stdlib-only (file parsing dispatches through the codec registry). ADR 0028.
package config

import (
	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// wrapAs returns the given config sentinel as the error origin (its
// code/reason/public win), attaching the cause's message as a structured field
// so it stays diagnosable without an *errs.Error cause hijacking the code. A nil
// cause yields the bare sentinel.
func wrapAs(sentinel *kerrs.Error, cause error) error {
	//: a nil cause needs no field — return the sentinel as-is.
	if cause == nil {
		//: the bare typed sentinel.
		return sentinel
	}
	//: the cause's message rides in the same field a stated refusal uses.
	return withCause(sentinel, cause.Error())
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
