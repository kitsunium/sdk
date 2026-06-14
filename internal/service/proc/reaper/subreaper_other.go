//go:build unix && !linux && !freebsd && !dragonfly

// Package reaper — non-Linux Unix subreaper stub for platforms with no
// reparent-here facility (darwin/openbsd/netbsd/solaris): neither Linux's
// prctl(PR_SET_CHILD_SUBREAPER) nor BSD's procctl(PROC_REAP_ACQUIRE) exists, so
// arming degrades to a typed error. FreeBSD and DragonFly are handled by the
// real procctl(2) sibling (subreaper_bsd.go).
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
