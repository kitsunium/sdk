//go:build !unix

package rlimit

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// applyLimits is the non-Unix stub: setrlimit(2) is a Unix mechanic with no
// portable equivalent on these targets, so every call degrades to the typed
// UnsupportedPlatform sentinel rather than acting or panicking.
func applyLimits(_ int, _ map[coreproc.Resource]coreproc.LimitValue) error {
	//: the bare sentinel carries the no-cause UNSUPPORTED_PLATFORM error.
	return coreproc.UnsupportedPlatform
}

// prepareLimits is the non-Unix stub: validation depends on the platform
// RLIMIT_* table, which exists only on Unix, so it reports the same
// UnsupportedPlatform error Apply would.
func prepareLimits(_ map[coreproc.Resource]coreproc.LimitValue) error {
	//: the bare sentinel carries the no-cause UNSUPPORTED_PLATFORM error.
	return coreproc.UnsupportedPlatform
}
