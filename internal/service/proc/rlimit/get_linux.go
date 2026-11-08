package rlimit

import (
	"syscall"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/service/proc/internal/rlim"
)

// syscallPrlimitRead names the read-back call for another pid.
const syscallPrlimitRead string = "prlimit64"

// getForeign reads another process's ceiling with prlimit64(2), new = nil.
func getForeign(pid, rl int, r coreproc.Resource) (coreproc.LimitValue, error) {
	var old syscall.Rlimit
	//: prlimit64(pid, resource, new=nil, old=&old): a read, nothing set.
	_, _, errno := syscall.Syscall6(syscall.SYS_PRLIMIT64, uintptr(pid), uintptr(rl), 0, uintptr(unsafe.Pointer(&old)), 0, 0)
	//: ESRCH for a gone pid, EPERM for another account's.
	if errno != 0 {
		return coreproc.LimitValue{}, readFailed(errno, pid, r.String(), syscallPrlimitRead)
	}
	soft, hard := rlim.Pair(old)
	//: the other process's ceiling.
	return coreproc.LimitValue{Soft: soft, Hard: hard}, nil
}
