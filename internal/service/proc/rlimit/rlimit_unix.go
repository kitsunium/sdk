//go:build unix && !linux

// Package rlimit — non-Linux Unix setrlimit(2) implementation. Darwin and the
// BSDs expose setrlimit(2) for the calling process exactly as Linux does, so a
// Spec's resource ceilings are honoured natively (applied post-fork on self by
// the exec trampoline). The Linux-only prlimit64(2) path for a FOREIGN pid has
// no portable equivalent here, so an Apply targeting another process degrades to
// the typed UnsupportedPlatform sentinel rather than silently limiting the
// caller. The Resource→RLIMIT_* table maps only the constants stdlib syscall
// exports on every target; RLIMIT_NPROC/RLIMIT_MEMLOCK live in golang.org/x/sys
// (banned), and RLIMIT_AS is absent on OpenBSD — both surface UnknownResource
// via the build-tagged table rather than a wrong limit or a build break.
package rlimit

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// exitOSErr is sysexits.h EX_OSERR (71), restated to carry the RLIMIT_FAILED
// sentinel's exit semantics when wrapping a setrlimit(2) errno without
// re-Defining the central code.
const exitOSErr int = 71

// syscallSetrlimit names the kernel entry point recorded as the "syscall" field
// on a failure so a log reflects the operation that actually failed.
const syscallSetrlimit string = "setrlimit"

// resourceToRLIMIT maps each abstract Resource to its platform RLIMIT_* constant
// for this target. Assembled via buildResourceLimits (not a literal) so the
// platform constants resolve under the unix tag and OpenBSD — which lacks
// RLIMIT_AS — drops that entry instead of failing to compile.
var resourceToRLIMIT = buildResourceLimits()

// buildResourceLimits returns the Resource→RLIMIT_* map for this Unix target,
// holding the constants stdlib syscall exports on every target plus the
// platform additions supplied by addPlatformLimits (RLIMIT_AS off OpenBSD).
func buildResourceLimits() map[coreproc.Resource]int {
	//: the RLIMIT_* constants stdlib exports on every supported non-Linux Unix.
	limits := map[coreproc.Resource]int{
		coreproc.ResourceNoFile: syscall.RLIMIT_NOFILE,
		coreproc.ResourceCore:   syscall.RLIMIT_CORE,
		coreproc.ResourceCPU:    syscall.RLIMIT_CPU,
		coreproc.ResourceFSize:  syscall.RLIMIT_FSIZE,
		coreproc.ResourceData:   syscall.RLIMIT_DATA,
		coreproc.ResourceStack:  syscall.RLIMIT_STACK,
	}
	//: add the platform-specific resources (RLIMIT_AS where the kernel has it).
	addPlatformLimits(limits)
	//: the assembled Resource→RLIMIT_* table for this platform.
	return limits
}

// resolve maps r to its platform RLIMIT_* constant, returning UnknownResource
// when the resource is unmapped on this target.
func resolve(r coreproc.Resource) (rl int, err error) {
	//: a mapped resource resolves to its RLIMIT_* constant.
	if rl, ok := resourceToRLIMIT[r]; ok {
		//: table hit — hand back the platform constant.
		return rl, nil
	}
	//: no mapping — the bare sentinel carries the no-cause UNKNOWN_RESOURCE.
	return 0, coreproc.UnknownResource
}

// prepareLimits validates that every Resource in limits maps to a platform
// RLIMIT_* constant without issuing a syscall, returning UnknownResource on the
// first unmapped resource and nil when all resources are honourable here.
func prepareLimits(limits map[coreproc.Resource]coreproc.LimitValue) error {
	//: surface the first unmapped resource so callers fail fast.
	for r := range limits {
		//: an unmapped resource is a usage error, not a syscall failure.
		if _, err := resolve(r); err != nil {
			//: propagate the typed UNKNOWN_RESOURCE sentinel unchanged.
			return err
		}
	}
	//: every resource mapped — nothing to apply yet.
	return nil
}

// applyLimits applies each soft/hard pair in limits to the process pid. Only the
// calling process (pid 0 or self) is supported: setrlimit(2) cannot target a
// foreign pid and this platform has no prlimit64(2), so a non-self pid yields
// UnsupportedPlatform. An unmapped resource yields UnknownResource and a failing
// syscall yields RlimitFailed.
func applyLimits(pid int, limits map[coreproc.Resource]coreproc.LimitValue) error {
	//: a foreign pid has no portable setrlimit path off Linux — refuse honestly.
	if pid != 0 && pid != syscall.Getpid() {
		//: the bare sentinel carries the no-cause UNSUPPORTED_PLATFORM error.
		return coreproc.UnsupportedPlatform
	}
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

// applyOne resolves r and applies the single soft/hard pair lv to the calling
// process via setrlimit(2), wrapping a failure in RlimitFailed annotated with
// the pid and resource name.
func applyOne(pid int, r coreproc.Resource, lv coreproc.LimitValue) error {
	//: resolve the platform constant before issuing any syscall.
	rl, err := resolve(r)
	//: an unmapped resource is a usage error, not a syscall failure.
	if err != nil {
		//: hand back the UNKNOWN_RESOURCE sentinel verbatim.
		return err
	}
	rlim := makeRlimit(lv.Soft, lv.Hard)
	//: setrlimit(2) operates on the calling process only.
	if serr := syscall.Setrlimit(rl, &rlim); serr != nil {
		//: surface the failure through the shared RLIMIT_FAILED wrapper.
		return rlimitFailed(serr, pid, r.String())
	}
	//: limit applied to the calling process.
	return nil
}

// rlimitFailed wraps a setrlimit(2) errno in the central RLIMIT_FAILED sentinel,
// annotated with the target pid, resource name, and the failing syscall. It
// restates the sentinel's fields verbatim; the code is never re-Defined here.
func rlimitFailed(cause error, pid int, resource string) error {
	//: restate the central RLIMIT_FAILED fields; never re-Define the code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeRlimitFailed,
		Reason:   "RLIMIT_FAILED",
		Public:   "Could not apply the resource limit",
		Private:  "service/proc/rlimit.Apply: setrlimit(2) failed",
		ExitCode: exitOSErr,
	}, errs.Int("pid", pid), errs.String("resource", resource), errs.String("syscall", syscallSetrlimit))
}
