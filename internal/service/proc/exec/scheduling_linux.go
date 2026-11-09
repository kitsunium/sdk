//go:build linux

package exec

import (
	"syscall"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// ioprioWhoProcessSel is IOPRIO_WHO_PROCESS and ioprioClassShift the class's
// place in an ioprio value, both from include/uapi/linux/ioprio.h.
const (
	ioprioWhoProcessSel uintptr = 1
	ioprioClassShift    int     = 13
)

// schedParam is struct sched_param: one int, the static priority.
type schedParam struct {
	priority int32
}

// checkSchedulingPlatform accepts every defined policy and class: Linux has
// them all.
func checkSchedulingPlatform(_ *coreproc.SchedulingValue) error {
	//: the kernel decides what this supervisor may grant.
	return nil
}

// applyScheduling sets sched's policy (sched_setscheduler) and, unless it is
// IOClassNone, its I/O priority (ioprio_set) on the live pid. Both apply to
// the child's main thread, which every thread it creates afterwards inherits:
// the window is the one Nice has, between the exec and this call. A refusal —
// a realtime policy or class without CAP_SYS_NICE / CAP_SYS_ADMIN, a priority
// out of range — is RLIMIT_FAILED.
func applyScheduling(pid int, sched *coreproc.SchedulingValue) error {
	//: nothing requested.
	if sched == nil {
		//: the inherited scheduling stands.
		return nil
	}
	param := new(schedParam{priority: int32(sched.Priority)})
	_, _, errno := syscall.Syscall(syscall.SYS_SCHED_SETSCHEDULER, uintptr(pid), uintptr(sched.Policy), uintptr(unsafe.Pointer(param)))
	//: the kernel refused the policy or its priority.
	if errno != 0 {
		//: the central RLIMIT_FAILED, naming the policy.
		return wrapRlimit(errno, errs.Int("pid", pid), errs.String("policy", sched.Policy.String()))
	}
	//: no I/O class requested: the kernel's default stands.
	if sched.IOClass == coreproc.IOClassNone {
		//: the policy alone.
		return nil
	}
	prio := int(sched.IOClass)<<ioprioClassShift | sched.IOPriority
	_, _, errno = syscall.Syscall(syscall.SYS_IOPRIO_SET, ioprioWhoProcessSel, uintptr(pid), uintptr(prio))
	//: the kernel refused the class or its level.
	if errno != 0 {
		//: the central RLIMIT_FAILED, naming the class.
		return wrapRlimit(errno, errs.Int("pid", pid), errs.String("io_class", sched.IOClass.String()))
	}
	//: both applied.
	return nil
}
