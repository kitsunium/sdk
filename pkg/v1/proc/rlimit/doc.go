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
//		"github.com/kitsunium/sdk/pkg/v1/proc/rlimit"
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
