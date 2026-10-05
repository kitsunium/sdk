package cgroup

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// Available reports whether the unified cgroup v2 hierarchy is mounted at
// /sys/fs/cgroup AND a sub-group can be created under it by the caller (the
// delegation test). It returns false off Linux, on a cgroup v1 host, and on a
// Linux host where the hierarchy is read-only to the caller.
func Available() bool {
	//: delegate to the platform probe chosen by build tag.
	return available()
}

// Create makes a new control group named name under the delegated cgroup v2
// root and returns a Group handle bound to it. It returns CgroupUnavailable when
// the hierarchy is absent or not delegated, CgroupCreateFailed when mkdir fails,
// and UnsupportedPlatform off Linux.
func Create(name string, opts ...Option) (g coreproc.Group, err error) {
	//: delegate to the platform constructor chosen by build tag.
	return createGroup(name, opts...)
}
