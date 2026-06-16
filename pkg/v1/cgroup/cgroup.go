//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/cgroup .

// Package cgroup creates and manages cgroup v2 control groups to confine a
// process tree's memory, CPU, pids, and IO.
//
// It is the thin public facade over internal/service/proc/cgroup: Group is an
// alias of the core proc.Group port and the functions delegate straight to the
// service implementation. The kernel-enforced control-group backend is the
// unified cgroup v2 hierarchy on Linux and a Job Object on Windows; the facade
// degrades gracefully — on a platform with no such backend (darwin, the BSDs), a
// cgroup v1 host, or an unprivileged/non-delegated Linux host it returns the
// typed UnsupportedPlatform or CgroupUnavailable error rather than panicking.
//
// # Usage
//
// Create a group, cap its memory, attach a process, then tear it down:
//
//	import (
//		"github.com/kitsunium/sdk/pkg/v1/cgroup"
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
// The backend is cgroup v2 on Linux and a Job Object on Windows. On those two,
// Available reports true (Linux additionally requires the hierarchy delegated)
// and Create returns a usable Group. On every other platform (darwin, the BSDs)
// Available returns false and Create returns UnsupportedPlatform. The Group
// surface is uniform; SetIOMax / Freeze / Thaw degrade to UnsupportedPlatform on
// the Job Object backend, which has no equivalent for them.
package cgroup

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svccgroup "github.com/kitsunium/sdk/internal/service/proc/cgroup"
)

// Group is a handle to a cgroup v2 control group: set controller ceilings,
// attach processes, and remove the group. It aliases the core proc.Group port.
type Group = coreproc.Group

// Option customises a Create call (functional-option pattern). It aliases the
// service Option type; construct options with WithRoot.
type Option = svccgroup.Option

// WithRoot overrides the parent directory under which Create makes the new
// control group, targeting a delegated sub-tree instead of the top-level mount.
func WithRoot(root string) Option {
	//: delegate verbatim to the service constructor.
	return svccgroup.WithRoot(root)
}

// Available reports whether a kernel-enforced control-group backend is usable: on
// Linux, that the unified cgroup v2 hierarchy is mounted AND a sub-group can be
// created under it by the caller; on Windows, that Job Objects are available
// (always true). It returns false on platforms with no backend (darwin, the
// BSDs), on cgroup v1, and on a Linux host where the hierarchy is read-only.
func Available() bool {
	//: delegate verbatim to the service probe.
	return svccgroup.Available()
}

// Create makes a new control group named name and returns a Group bound to it —
// a cgroup v2 sub-group under the delegated root on Linux, a Job Object on
// Windows. It returns CgroupUnavailable when the Linux hierarchy is absent or not
// delegated, CgroupCreateFailed when creation fails, and UnsupportedPlatform on a
// platform with no control-group backend (darwin, the BSDs).
func Create(name string, opts ...Option) (g Group, err error) {
	//: delegate verbatim to the service constructor.
	return svccgroup.Create(name, opts...)
}

// MustCreate is like [Create] but panics with the typed error when creation
// fails — UnsupportedPlatform on a platform with no control-group backend
// (darwin, the BSDs), or CgroupUnavailable when the Linux hierarchy is not
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
