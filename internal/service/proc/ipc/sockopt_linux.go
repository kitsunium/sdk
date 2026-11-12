//go:build linux && !386

package ipc

import "syscall"

// getsockoptTrap is getsockopt(2)'s system call.
const getsockoptTrap uintptr = syscall.SYS_GETSOCKOPT
