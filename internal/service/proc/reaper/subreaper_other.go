//go:build unix && !linux

// Package reaper — non-Linux Unix (darwin/bsd) subreaper stub: no
// PR_SET_CHILD_SUBREAPER equivalent exists, so arming degrades to a typed error.
package reaper

import coreproc "github.com/kitsunium/sdk/internal/core/proc"

// SetChildSubreaper reports UnsupportedPlatform on non-Linux Unix systems
// (darwin/bsd), which have no prctl(PR_SET_CHILD_SUBREAPER) equivalent. The
// SIGCHLD reaping loop still works there; only the subreaper attribute is
// unavailable, so orphans reparent to PID1 rather than to this process.
func SetChildSubreaper() error {
	//: no prctl subreaper facility off Linux — return the central sentinel.
	return coreproc.UnsupportedPlatform
}
