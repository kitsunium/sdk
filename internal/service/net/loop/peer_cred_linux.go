//go:build linux && !386

package loop

import (
	"syscall"
	"unsafe"
)

// peerCred reads SO_PEERCRED of fd into a structure that stays on the stack:
// the standard library's GetsockoptUcred returns a pointer to a heap one, an
// allocation a loop's callback does not pay.
func peerCred(fd int) (syscall.Ucred, bool) {
	var u syscall.Ucred
	n := new(uint32(unsafe.Sizeof(u)))
	_, _, errno := syscall.Syscall6(syscall.SYS_GETSOCKOPT, uintptr(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED, uintptr(unsafe.Pointer(&u)), uintptr(unsafe.Pointer(n)), 0)
	//: whatever the kernel answered.
	return u, errno == 0
}
