//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/proc/cgroup .

// Package cgroup creates and manages cgroup v2 control groups to confine a
// process tree's memory, CPU, pids, and IO.
//
// It is the thin public facade over internal/service/proc/cgroup: Group is an
// alias of the core proc.Group port and the functions delegate straight to the
// service implementation. The kernel-enforced control-group backend is the
// unified cgroup v2 hierarchy on Linux, a Job Object on Windows and rctl(8) on
// FreeBSD; the facade degrades gracefully — on a platform with no such backend
// (darwin, OpenBSD, NetBSD, DragonFly, illumos, Solaris, a FreeBSD kernel
// without RACCT), a cgroup
// v1 host, or an unprivileged/non-delegated Linux host it returns the typed
// UnsupportedPlatform or CgroupUnavailable error rather than panicking.
//
// # Usage
//
// Create a group, cap its memory, attach a process, then tear it down:
//
//	import (
//		"github.com/kitsunium/sdk/pkg/v1/proc/cgroup"
//	)
//
//	if !cgroup.Available() {
//		// cgroup v2 not mounted or not delegated to this caller.
//		return
//	}
//	g, err := cgroup.Create("myservice")
//	if err != nil {
//		return // CgroupUnavailable / CgroupCreateFailed / UnsupportedPlatform
//	}
//	defer g.Delete()
//	_ = g.SetMemoryMax(256 << 20) // 256 MiB; a negative value means "no limit"
//	_ = g.SetCPUMax(50000, 100000) // 50% of one CPU ("quota period" microseconds)
//	_ = g.Add(pid)                 // move pid into the group via cgroup.procs
//
// # Semantics
//
// Set*Max writes the matching cgroup v2 interface file: memory.max, cpu.max
// ("quota period"), pids.max, and one io.max line written verbatim. A negative
// numeric value writes the literal "max" (no limit). Add writes a pid to
// cgroup.procs. Delete rmdir's the group, which the kernel refuses (EBUSY,
// surfaced as CgroupDeleteFailed) until every process has left it.
//
// # Delegation
//
// Available is honest about delegation: it confirms the cgroup.controllers
// marker exists AND that a throwaway sub-directory can be created under the
// mount. A read-only root (the common unprivileged-container case) reports
// false, and Create returns CgroupUnavailable. Use WithRoot to target a
// delegated sub-tree handed to the caller by a container manager.
//
// # Platform notes
//
// The backend is cgroup v2 on Linux, a Job Object on Windows and rctl(8) on
// FreeBSD. On those, Available reports true (Linux additionally requires the
// hierarchy delegated, FreeBSD a kernel with RACCT) and Create returns a usable
// Group. On every other platform (darwin, OpenBSD, NetBSD, DragonFly, illumos, Solaris) Available
// returns false and Create returns UnsupportedPlatform. The Group surface is
// uniform; SetIOMax / Freeze / Thaw degrade to UnsupportedPlatform on the Job
// Object backend, which has no equivalent for them. WithRoot names a directory of
// the cgroup v2 hierarchy, so it takes effect on Linux only: the Job Object and
// rctl backends have no hierarchy, and accept the option and ignore it.
package cgroup

// MustCreate is like [Create] but panics with the typed error when creation
// fails — UnsupportedPlatform on a platform with no control-group backend
// (darwin, OpenBSD, NetBSD, DragonFly, illumos, Solaris), or CgroupUnavailable when the Linux hierarchy is not
// delegated. It is the idiomatic Go MustX opt-in (like
// [regexp.MustCompile]) for a consumer that chooses crash-on-unsupported at its
// own startup; the SDK itself never panics, and [Create] is the non-panicking
// form for normal use. The panic value is the typed error, so a top-level
// recover() can classify it via errs.CodeOf / HasCode.
func MustCreate(name string, opts ...Option) Group {
	grp, err := Create(name, opts...)
	//: a failed Create is the consumer's chosen crash point.
	if err != nil {
		//: panic with the typed error value, never a bare string.
		panic(err)
	}
	//: the live group on success.
	return grp
}
