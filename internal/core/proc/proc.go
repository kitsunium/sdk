// Package proc declares the OS process-supervision contract of the SDK: the
// ports (Process, Reaper, Group, Listener), the immutable value types (Spec,
// ExitValue, LimitValue, NotificationValue, Signal, Resource), and the domain's
// complete error-sentinel set (range 0.2.6.*). It is the sixth internal/core
// sibling (ADR 0016), peer of codec / writer / crypto / logger / transform.
//
// Unlike codec and crypto, proc ships no plug-in registry: each primitive has a
// single canonical OS implementation chosen at build time by platform tag, not
// a runtime-registered scheme. Core declares the ports and value types; concrete
// behaviour lives in internal/service/proc/*; the pkg/v1 facades (process,
// signal, reaper, rlimit, cgroup, sdnotify) re-export this surface.
//
// proc is stdlib-only (os, syscall, time, strconv, strings) plus
// internal/kernel/errs — no golang.org/x/sys — preserving the SDK's dep-light
// invariant. Every error returned by the domain is one of the sentinels in
// errors.go; service code wraps them with errs.Wrap and never defines new codes.
package proc

// Resource identifies a per-process resource governed by setrlimit(2). It is an
// abstract, platform-portable enum; the service layer maps each value to the
// platform's RLIMIT_* constant. The zero value ResourceUnknown is the reserved
// invalid sentinel.
type Resource int

const (
	// ResourceUnknown is the reserved zero value: no resource selected.
	ResourceUnknown Resource = iota
	// ResourceNoFile limits the highest open file descriptor (RLIMIT_NOFILE),
	// mapping systemd's LimitNOFILE=.
	ResourceNoFile
	// ResourceNProc limits the number of processes for the real user id
	// (RLIMIT_NPROC), mapping systemd's LimitNPROC=.
	ResourceNProc
	// ResourceCore limits the size of a core dump in bytes (RLIMIT_CORE),
	// mapping systemd's LimitCORE=.
	ResourceCore
	// ResourceAS limits the process virtual address-space size (RLIMIT_AS),
	// mapping systemd's LimitAS=.
	ResourceAS
	// ResourceCPU limits CPU time in seconds (RLIMIT_CPU), mapping systemd's
	// LimitCPU=.
	ResourceCPU
	// ResourceFSize limits the largest file the process may create in bytes
	// (RLIMIT_FSIZE), mapping systemd's LimitFSIZE=.
	ResourceFSize
	// ResourceData limits the process data-segment size (RLIMIT_DATA), mapping
	// systemd's LimitDATA=.
	ResourceData
	// ResourceStack limits the process stack size (RLIMIT_STACK), mapping
	// systemd's LimitSTACK=.
	ResourceStack
	// ResourceMemLock limits bytes that may be locked into RAM (RLIMIT_MEMLOCK),
	// mapping systemd's LimitMEMLOCK=.
	ResourceMemLock
)

// resourceNames maps each Resource to the lowercase suffix of its systemd
// Limit* directive. Declared as a literal (no init) so String stays a flat
// lookup rather than a high-complexity switch.
var resourceNames = map[Resource]string{
	ResourceNoFile:  "nofile",
	ResourceNProc:   "nproc",
	ResourceCore:    "core",
	ResourceAS:      "as",
	ResourceCPU:     "cpu",
	ResourceFSize:   "fsize",
	ResourceData:    "data",
	ResourceStack:   "stack",
	ResourceMemLock: "memlock",
}

// String reports the canonical lowercase name of r (e.g. "nofile"), or
// "unknown" for an unrecognised value. The names match the suffix of the
// systemd Limit* directives.
func (r Resource) String() string {
	//: a known resource resolves to its directive suffix.
	if name, ok := resourceNames[r]; ok {
		//: table hit — return the directive suffix verbatim.
		return name
	}
	//: the zero value and any out-of-range value report as unknown, not a raw int.
	return "unknown"
}

// Known reports whether r is a defined resource other than the zero value.
func (r Resource) Known() bool {
	//: defined resources occupy the contiguous range above the zero value.
	return r > ResourceUnknown && r <= ResourceMemLock
}
