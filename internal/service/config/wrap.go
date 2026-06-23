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
	//: wrap the sentinel (origin-wins keeps its code) + carry the cause message.
	return kerrs.Wrap(sentinel, kerrs.WrapParams{}, kerrs.String("cause", cause.Error()))
}

// keep the core import referenced even if a source file is built alone.
var _ = coreconfig.ConfigSourceFailed
