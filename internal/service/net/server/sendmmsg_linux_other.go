//go:build linux && !amd64 && !386

package server

import "syscall"

// sysSendmmsg is sendmmsg(2)'s number, as the standard library's syscall
// package defines it on every Linux architecture but amd64 and 386.
const sysSendmmsg uintptr = syscall.SYS_SENDMMSG
