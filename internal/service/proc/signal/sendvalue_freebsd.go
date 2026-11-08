package signal

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// sendValue queues sig with value through sigqueue(2), whose union sigval the
// syscall takes as one pointer-sized word (sys/kern/syscalls.master: 456).
func sendValue(target Target, sig coreproc.Signal, value int) error {
	//: sigqueue(pid, signo, value).
	_, _, errno := syscall.Syscall(syscall.SYS_SIGQUEUE, uintptr(target), uintptr(sig.Int()), uintptr(value))
	//: ESRCH for a reaped pid, EPERM for another account's, EINVAL for a bad signal.
	if errno != 0 {
		return sendFailed(errno, target, sig)
	}
	//: queued.
	return nil
}
