//go:build linux

// Package server — SO_REUSEPORT support on Linux.
package server

import "syscall"

// soReusePort is SO_REUSEPORT.
//
// It is absent from the Go standard library's syscall package, so the value is
// hand-defined and cited here — the discipline ADR 0018 established for ABI
// constants, adopted because golang.org/x/sys is banned SDK-wide. Linux defines
// it in include/asm-generic/socket.h as 15.
const soReusePort int = 0xf

// reusePortSupported reports whether this platform can bind several listeners
// to one address.
func reusePortSupported() bool {
	//: Linux has had SO_REUSEPORT with load balancing since 3.9.
	return true
}

// setReusePort enables SO_REUSEPORT on a socket about to be bound.
func setReusePort(fd uintptr) error {
	//: must be set before bind, which is why it runs from the Control hook.
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePort, 1)
}
