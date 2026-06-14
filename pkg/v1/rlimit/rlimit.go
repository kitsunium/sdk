//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/rlimit .

// Package rlimit applies per-process resource ceilings via setrlimit(2) /
// prlimit64(2).
//
// It is the thin public facade over internal/service/proc/rlimit: the types are
// aliases of the core proc value types and the functions delegate straight to
// the service implementation. Resources are named abstractly (Resource) and
// mapped to the platform RLIMIT_* constant inside the service layer, so calling
// code stays portable; on a non-Linux host every call returns the typed
// UnsupportedPlatform error rather than acting.
//
// # Usage
//
// Lower the open-file ceiling of the current process to 1024 soft / 4096 hard:
//
//	import (
//		"github.com/kitsunium/sdk/pkg/v1/rlimit"
//	)
//
//	err := rlimit.Apply(0, map[rlimit.Resource]rlimit.Limit{
//		rlimit.ResourceNoFile: {Soft: 1024, Hard: 4096},
//	})
//	if err != nil {
//		// inspect with errs.HasCode(err, ...) — RlimitFailed / UnknownResource.
//	}
//
// A pid of 0 targets the calling process via setrlimit(2). Any other pid targets
// that process via prlimit64(2), which requires the CAP_SYS_RESOURCE capability;
// without it the call returns RlimitFailed wrapping EPERM.
//
// # Spec integration
//
// Go's os/exec SysProcAttr carries no rlimit field. process.Start applies a
// Spec.Rlimits set to the child it spawns via a re-exec trampoline; this package
// is the standalone primitive for the other cases: apply limits from the child
// after fork (a pid-0 Apply in the child) or, when supervising, apply them to the
// spawned pid via prlimit64. PrepareSysProcAttr validates a limit set without any
// syscall — call it at Spec-construction time to fail fast on a Resource this
// platform cannot honour, before a process is ever spawned.
//
// # Platform notes
//
// Only Linux is supported. RLIMIT_NPROC and RLIMIT_MEMLOCK are absent from Go's
// syscall package and are restated from the kernel generic ABI inside the
// service layer. Off Linux, Apply and PrepareSysProcAttr return
// UnsupportedPlatform.
package rlimit

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svcrlimit "github.com/kitsunium/sdk/internal/service/proc/rlimit"
)

// LimitInfinity is the soft/hard value meaning "no limit" (RLIM_INFINITY).
const LimitInfinity uint64 = coreproc.LimitInfinity

// ResourceNoFile limits the highest open file descriptor (RLIMIT_NOFILE). It
// re-exports the core enum value so consumers qualify it as rlimit.ResourceNoFile.
const ResourceNoFile Resource = coreproc.ResourceNoFile

// ResourceNProc limits the number of processes for the real uid (RLIMIT_NPROC).
const ResourceNProc Resource = coreproc.ResourceNProc

// ResourceCore limits the core-dump size in bytes (RLIMIT_CORE).
const ResourceCore Resource = coreproc.ResourceCore

// ResourceAS limits the process virtual address-space size (RLIMIT_AS).
const ResourceAS Resource = coreproc.ResourceAS

// ResourceCPU limits CPU time in seconds (RLIMIT_CPU).
const ResourceCPU Resource = coreproc.ResourceCPU

// ResourceFSize limits the largest file the process may create (RLIMIT_FSIZE).
const ResourceFSize Resource = coreproc.ResourceFSize

// ResourceData limits the process data-segment size (RLIMIT_DATA).
const ResourceData Resource = coreproc.ResourceData

// ResourceStack limits the process stack size (RLIMIT_STACK).
const ResourceStack Resource = coreproc.ResourceStack

// ResourceMemLock limits bytes that may be locked into RAM (RLIMIT_MEMLOCK).
const ResourceMemLock Resource = coreproc.ResourceMemLock

// Resource is the abstract, platform-portable resource enum mapped to a
// RLIMIT_* constant by the service layer. It aliases the core proc type.
type Resource = coreproc.Resource

// Limit is an immutable soft/hard setrlimit(2) pair. It aliases the core proc
// LimitValue type.
type Limit = coreproc.LimitValue

// Apply sets the soft/hard ceilings in limits on the process identified by pid.
// A pid of 0 targets the calling process; any other pid targets that process via
// prlimit64(2) and needs CAP_SYS_RESOURCE. It returns UnknownResource for an
// unmapped resource, RlimitFailed on a syscall failure, and UnsupportedPlatform
// off Linux.
func Apply(pid int, limits map[Resource]Limit) error {
	//: delegate verbatim to the service implementation.
	return svcrlimit.Apply(pid, limits)
}

// PrepareSysProcAttr validates limits without issuing any syscall and reports
// whether they can be applied on this platform, returning the same typed errors
// Apply would (UnknownResource / UnsupportedPlatform). Use it to fail fast at
// Spec-construction time; Go's SysProcAttr has no rlimit field, so the actual
// application happens post-fork via Apply.
func PrepareSysProcAttr(limits map[Resource]Limit) error {
	//: delegate verbatim to the service implementation.
	return svcrlimit.PrepareSysProcAttr(limits)
}
