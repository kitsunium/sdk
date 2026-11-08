package rlimit

import (
	"syscall"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The sysctl(3) name of a process's open descriptor count, kern.proc.nfds
// (sys/sysctl.h): /dev/fd lists only 0, 1 and 2 unless fdescfs is mounted.
const (
	ctlKern       int32 = 1
	kernProc      int32 = 14
	kernProcNFDs  int32 = 43
	sysctlNameLen int   = 4
)

// openFiles asks the kernel for the caller's descriptor count.
func openFiles() (int, error) {
	mib := [sysctlNameLen]int32{ctlKern, kernProc, kernProcNFDs, int32(syscall.Getpid())}
	var count int32
	size := unsafe.Sizeof(count)
	//: __sysctl(name, namelen, old, oldlenp, nil, 0).
	_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
		uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&size)), 0, 0)
	//: a kernel without the node (before FreeBSD 13).
	if errno != 0 {
		return 0, errs.Wrap(errno, errs.WrapParams{
			Code:     coreproc.CodeRlimitFailed,
			Reason:   "RLIMIT_FAILED",
			Public:   "Could not read the resource limit",
			Private:  "service/proc/rlimit.OpenFiles: sysctl kern.proc.nfds failed",
			ExitCode: exitOSErr,
		})
	}
	//: the kernel's count.
	return int(count), nil
}
