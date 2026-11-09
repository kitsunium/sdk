//go:build !linux && !solaris && !darwin && !freebsd && !windows

package rlimit

import coreproc "github.com/kitsunium/sdk/internal/core/proc"

// openFiles is refused where the SDK binds no descriptor count: OpenBSD and
// NetBSD keep static /dev/fd nodes, DragonFly and the rest are unverified.
func openFiles() (int, error) {
	//: the uniform answer of a platform without the mechanic.
	return 0, coreproc.UnsupportedPlatform
}
