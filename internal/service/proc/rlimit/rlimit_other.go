//go:build !linux

// Package rlimit — non-Linux stub returning UnsupportedPlatform.
package rlimit

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// applyLimits is the non-Linux stub: setrlimit/prlimit64 semantics are provided
// only by the Linux implementation, so every call degrades to the typed
// UnsupportedPlatform sentinel rather than acting or panicking.
func applyLimits(_ int, _ map[coreproc.Resource]coreproc.LimitValue) error {
	//: the bare sentinel carries the no-cause UNSUPPORTED_PLATFORM error.
	return coreproc.UnsupportedPlatform
}

// prepareLimits is the non-Linux stub: validation depends on the platform
// RLIMIT_* table, which exists only on Linux, so it reports the same
// UnsupportedPlatform error Apply would.
func prepareLimits(_ map[coreproc.Resource]coreproc.LimitValue) error {
	//: the bare sentinel carries the no-cause UNSUPPORTED_PLATFORM error.
	return coreproc.UnsupportedPlatform
}
