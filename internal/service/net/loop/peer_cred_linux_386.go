//go:build linux && 386

package loop

import "syscall"

// peerCred reads SO_PEERCRED of fd. linux/386 has no getsockopt system call, only
// socketcall, whose arguments are an array of integers a stack that moves
// would leave stale: the standard library's GetsockoptUcred is used, and its
// heap structure is the one allocation of a call there.
func peerCred(fd int) (syscall.Ucred, bool) {
	u, err := syscall.GetsockoptUcred(fd, syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	//: refused.
	if err != nil {
		//: unknown.
		return syscall.Ucred{}, false
	}
	//: as the kernel recorded it.
	return *u, true
}
