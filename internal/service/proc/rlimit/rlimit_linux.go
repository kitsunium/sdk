//go:build linux

// Package rlimit — Linux setrlimit/prlimit64 implementation.
package rlimit

import (
	"syscall"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// exitOSErr is sysexits.h EX_OSERR (71), the exit status the RLIMIT_FAILED
// sentinel carries; restated here to wrap a stdlib cause with matching
// semantics without re-Defining the central code.
const exitOSErr int = 71

// rlimitNProc is RLIMIT_NPROC on the Linux generic ABI; Go's syscall package
// omits it, so the constant is restated here (asm-generic/resource.h).
const rlimitNProc int = 6

// rlimitMemLock is RLIMIT_MEMLOCK on the Linux generic ABI; Go's syscall
// package omits it, so the constant is restated here (asm-generic/resource.h).
const rlimitMemLock int = 8

// resourceToRLIMIT maps each abstract Resource to the Linux RLIMIT_* constant.
// Declared as a var literal (no init) per KTN-FUNC-NOINIT; the two values absent
// from syscall are supplied via the restated constants above.
var resourceToRLIMIT = map[coreproc.Resource]int{
	coreproc.ResourceNoFile:  syscall.RLIMIT_NOFILE,
	coreproc.ResourceNProc:   rlimitNProc,
	coreproc.ResourceCore:    syscall.RLIMIT_CORE,
	coreproc.ResourceAS:      syscall.RLIMIT_AS,
	coreproc.ResourceCPU:     syscall.RLIMIT_CPU,
	coreproc.ResourceFSize:   syscall.RLIMIT_FSIZE,
	coreproc.ResourceData:    syscall.RLIMIT_DATA,
	coreproc.ResourceStack:   syscall.RLIMIT_STACK,
	coreproc.ResourceMemLock: rlimitMemLock,
}

// resolve maps r to its Linux RLIMIT_* constant, returning UnknownResource when
// the resource has no mapping on this platform.
func resolve(r coreproc.Resource) (rl int, err error) {
	//: a mapped resource resolves to its RLIMIT_* constant.
	if rl, ok := resourceToRLIMIT[r]; ok {
		//: table hit — hand back the platform constant.
		return rl, nil
	}
	//: no mapping — the bare sentinel carries the no-cause UNKNOWN_RESOURCE.
	return 0, coreproc.UnknownResource
}

// prepareLimits validates that every Resource in limits maps to an RLIMIT_*
// constant, performing no syscall. It returns UnknownResource on the first
// unmapped resource and nil when all resources are honourable on this platform.
func prepareLimits(limits map[coreproc.Resource]coreproc.LimitValue) error {
	//: an empty/nil map is trivially honourable.
	for r := range limits {
		//: surface the first unmapped resource so callers fail fast.
		if _, err := resolve(r); err != nil {
			//: propagate the typed UNKNOWN_RESOURCE sentinel unchanged.
			return err
		}
	}
	//: every resource mapped — nothing to apply yet.
	return nil
}

// applyLimits applies each soft/hard pair in limits to the process pid. pid 0
// (or self) uses setrlimit(2); any other pid uses prlimit64(2). It returns
// UnknownResource for an unmapped resource and RlimitFailed wrapping the syscall
// errno on failure.
func applyLimits(pid int, limits map[coreproc.Resource]coreproc.LimitValue) error {
	//: apply each requested ceiling, stopping at the first failure.
	for r, lv := range limits {
		//: stop at the first unmapped or failing resource.
		if err := applyOne(pid, r, lv); err != nil {
			//: propagate the already-typed error unchanged.
			return err
		}
	}
	//: every ceiling applied successfully.
	return nil
}

// applyOne resolves r and applies the single soft/hard pair lv to pid, routing
// to setrlimit for the calling process and prlimit64 for any other pid.
func applyOne(pid int, r coreproc.Resource, lv coreproc.LimitValue) error {
	//: resolve the platform constant before issuing any syscall.
	rl, err := resolve(r)
	//: an unmapped resource is a usage error, not a syscall failure.
	if err != nil {
		//: hand back the UNKNOWN_RESOURCE sentinel verbatim.
		return err
	}
	rlim := syscall.Rlimit{Cur: lv.Soft, Max: lv.Hard}
	//: pid 0 and the caller's own pid take the simpler setrlimit path.
	if pid == 0 || pid == syscall.Getpid() {
		//: setrlimit(2) operates on the calling process only.
		return applySelf(pid, r.String(), rl, &rlim)
	}
	//: a foreign pid requires prlimit64(2) and CAP_SYS_RESOURCE.
	return applyForeign(pid, r.String(), rl, &rlim)
}

// rlimitFailed wraps a syscall cause in the central RLIMIT_FAILED sentinel,
// annotated with the target pid and resource name. It restates the sentinel's
// fields verbatim; the code is never re-Defined here.
func rlimitFailed(cause error, pid int, resource string) error {
	//: restate the central RLIMIT_FAILED fields; never re-Define the code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeRlimitFailed,
		Reason:   "RLIMIT_FAILED",
		Public:   "Could not apply the resource limit",
		Private:  "service/proc/rlimit.Apply: setrlimit(2) failed",
		ExitCode: exitOSErr,
	}, errs.Int("pid", pid), errs.String("resource", resource))
}

// applySelf applies rlim to the calling process via setrlimit(2), wrapping a
// failure in RlimitFailed annotated with the pid and resource name.
func applySelf(pid int, resource string, rl int, rlim *syscall.Rlimit) error {
	//: setrlimit(2) targets the calling process — no pid argument.
	if err := syscall.Setrlimit(rl, rlim); err != nil {
		//: surface the failure through the shared RLIMIT_FAILED wrapper.
		return rlimitFailed(err, pid, resource)
	}
	//: limit applied to the calling process.
	return nil
}

// applyForeign applies rlim to another process via prlimit64(2), wrapping a
// failure in RlimitFailed annotated with the pid and resource name.
func applyForeign(pid int, resource string, rl int, rlim *syscall.Rlimit) error {
	//: prlimit64(pid, resource, new, old) — new=rlim, old=nil (no read-back).
	_, _, errno := syscall.Syscall6(
		syscall.SYS_PRLIMIT64,
		uintptr(pid),
		uintptr(rl),
		uintptr(unsafe.Pointer(rlim)),
		0, 0, 0,
	)
	//: a non-zero errno is a genuine syscall failure.
	if errno != 0 {
		//: surface the errno through the shared RLIMIT_FAILED wrapper.
		return rlimitFailed(errno, pid, resource)
	}
	//: limit applied to the foreign process.
	return nil
}
