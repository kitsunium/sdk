package cgroup

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// Available reports whether the unified cgroup v2 hierarchy is mounted at
// /sys/fs/cgroup AND a sub-group can be created under it by the caller (the
// delegation test). It returns false off Linux, on a cgroup v1 host, and on a
// Linux host where the hierarchy is read-only to the caller. It answers for
// the mount root only: for a delegated subtree, use Probe.
func Available() bool {
	//: delegate to the platform probe chosen by build tag.
	return available()
}

// Create makes a new control group named name under the delegated cgroup v2
// root and returns a Group handle bound to it. On Linux name may be a nested
// path, "a/b", whose missing groups are made on the way; a name that is
// absolute, not canonical, climbs with "..", or crosses a link is InvalidSpec,
// and nothing is ever made or written outside the root. It returns
// CgroupUnavailable when the hierarchy is absent or not delegated,
// CgroupControllerAbsent when WithControllers names a controller the
// root does not offer, CgroupCreateFailed when mkdir fails, and
// UnsupportedPlatform off Linux.
func Create(name string, opts ...Option) (g coreproc.Group, err error) {
	//: delegate to the platform constructor chosen by build tag.
	return createGroup(name, opts...)
}

// ProbeValue is what Probe found under a control-group root: whether it is a
// cgroup v2 directory, whether the caller may create a group under it, its
// controllers, and the caller's own cgroup.
type ProbeValue struct {
	// Root is the directory probed.
	Root string
	// OwnGroup is the directory of this process's own unified cgroup — the
	// root a delegated caller passes back as WithRoot — or empty where there
	// is none to name.
	OwnGroup string
	// Controllers are the controllers Root's cgroup.controllers lists.
	Controllers []string
	// Mounted reports that Root is a cgroup v2 directory.
	Mounted bool
	// Delegated reports that the caller may create a group under Root: a
	// uniquely named one was created and removed.
	Delegated bool
}

// Probe reports what the caller may do under a control-group root: WithRoot's,
// else the unified mount. Where Available answers for the mount root alone —
// false on every host that delegates a subtree rather than the root, a
// systemd unit with Delegate=yes, a rootless container, a user session —
// Probe answers for the root it is given, and names the caller's own cgroup
// to pass back as WithRoot. On Linux it never fails: a root without the
// cgroup.controllers marker is not Mounted. On Windows (Job Objects) and
// FreeBSD (rctl, with RACCT) Mounted and Delegated are Available's answer and
// OwnGroup is empty: neither has a hierarchy to delegate. Elsewhere it
// returns UnsupportedPlatform.
func Probe(opts ...Option) (ProbeValue, error) {
	//: delegate to the platform probe chosen by build tag.
	return probe(applyOptions(opts))
}

// Membership returns the cgroup v2 path /proc/<pid>/cgroup names for a
// process of the group whose directory is dir — the path coreproc's
// AttachValue.Cgroup bounds an adoption with —, read off the cgroup2 mount
// that holds dir: "/sys/fs/cgroup/system.slice/web.service" is
// "/system.slice/web.service", and under a mount that exposes a subtree the
// path is that subtree's. GroupTree.Path is a directory, never this path.
// It returns InvalidSpec when dir is not absolute and clean or no cgroup2
// mount holds it, and UnsupportedPlatform off Linux.
func Membership(dir string) (string, error) {
	//: delegate to the platform reading chosen by build tag.
	return membership(dir)
}
