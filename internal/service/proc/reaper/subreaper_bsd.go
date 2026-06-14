//go:build freebsd || dragonfly

// Package reaper — FreeBSD / DragonFly BSD descendant-reaper arming via
// procctl(2). procctl(P_PID, 0, PROC_REAP_ACQUIRE, NULL) makes the calling
// process the reaper for its whole descendant tree, the BSD analogue of Linux's
// prctl(PR_SET_CHILD_SUBREAPER): orphaned descendants reparent to this process
// instead of PID1, so a non-init supervisor still receives their SIGCHLD.
package reaper

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// idtypePID is the idtype_t value P_PID, the first member of the idtype enum in
// <sys/wait.h> and therefore 0 on both FreeBSD and DragonFly (verified against
// freebsd-src and DragonFlyBSD sys/sys/wait.h — P_PID is the leading enumerator).
// It selects "the process named by id" as the procctl(2) target. It is typed
// uintptr to feed syscall.Syscall6 directly.
const idtypePID uintptr = 0

// procReapAcquire is the procctl(2) cmd that enables descendant reaping for the
// target process. The numeric value differs between the two BSDs and is NOT
// exported by the stdlib syscall package, so it is named per-platform in the
// build-tagged siblings (subreaper_freebsd.go = 2, subreaper_dragonfly.go = 1)
// alongside sysProcctl, the procctl(2) syscall number.

// targetSelf is the id passed to procctl(2) with cmd=PROC_REAP_ACQUIRE: 0 means
// "the calling process itself" per the procctl(2) semantics, so the current
// process becomes the reaper for its own future descendants. data is NULL for
// ACQUIRE (the procctl(2) man page: "The data argument is ignored and can be
// NULL"), expressed as a 0 uintptr.
const targetSelf uintptr = 0

// SetChildSubreaper marks the calling process as the reaper for its descendant
// tree via procctl(P_PID, 0, PROC_REAP_ACQUIRE, NULL) so orphaned descendants
// reparent here rather than to PID1. It returns SubreaperFailed (wrapping the
// errno) on failure. It is the FreeBSD / DragonFly analogue of the Linux
// prctl(PR_SET_CHILD_SUBREAPER) path; platforms with neither facility report
// UnsupportedPlatform.
func SetChildSubreaper() error {
	//: procctl(2) takes four args (idtype, id, cmd, data); data is NULL for
	//: ACQUIRE and arg5..arg6 must be passed as explicit zeros (Syscall6) so the
	//: unused slots never carry stale register contents that fail with EINVAL.
	_, _, errno := syscall.Syscall6(sysProcctl, idtypePID, targetSelf, procReapAcquire, 0, 0, 0)
	//: a zero errno is success — descendant reaping is now enabled.
	if errno == 0 {
		//: nothing to report on success.
		return nil
	}
	//: a non-zero errno failed to acquire reaper mode — wrap the central sentinel
	//: with the same SUBREAPER_FAILED contract the Linux prctl path returns.
	return errs.Wrap(errno, errs.WrapParams{
		Code:     coreproc.CodeSubreaperFailed,
		Reason:   "SUBREAPER_FAILED",
		Public:   "Could not enable child-subreaper mode",
		Private:  "service/proc/reaper.SetChildSubreaper: procctl(PROC_REAP_ACQUIRE) failed",
		ExitCode: exitOSErr,
	})
}
