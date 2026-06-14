//go:build linux

// Package reaper — Linux PR_SET_CHILD_SUBREAPER arming via prctl(2).
package reaper

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// prSetChildSubreaper is the prctl(2) operation that marks the calling process
// as a "child subreaper": orphaned descendants reparent to the nearest living
// ancestor subreaper instead of PID1, so a non-init supervisor still receives
// their SIGCHLD and can wait(2) them. The value is 36 on Linux and is not
// exported by the standard syscall package, so it is named here. It is typed
// uintptr to feed syscall.Syscall directly.
const prSetChildSubreaper uintptr = 36

// SetChildSubreaper marks the calling process as a child subreaper via
// prctl(PR_SET_CHILD_SUBREAPER, 1) so orphaned descendants reparent here rather
// than to PID1. It returns SubreaperFailed (wrapping the errno) on failure. It
// is Linux-only; other Unix platforms have no equivalent and report
// UnsupportedPlatform.
func SetChildSubreaper() error {
	//: arg2=1 enables subreaper mode; the remaining prctl args are unused (0).
	_, _, errno := syscall.Syscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0)
	//: a zero errno is success — subreaper mode is now armed.
	if errno == 0 {
		//: nothing to report on success.
		return nil
	}
	//: a non-zero errno failed to arm subreaper mode — wrap the central sentinel.
	return errs.Wrap(errno, errs.WrapParams{
		Code:     coreproc.CodeSubreaperFailed,
		Reason:   "SUBREAPER_FAILED",
		Public:   "Could not enable child-subreaper mode",
		Private:  "service/proc/reaper.SetChildSubreaper: prctl(PR_SET_CHILD_SUBREAPER) failed",
		ExitCode: exitOSErr,
	})
}
