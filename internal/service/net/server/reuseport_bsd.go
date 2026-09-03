//go:build darwin || freebsd || netbsd || openbsd || dragonfly

// Package server — SO_REUSEPORT support on the BSD family.
package server

import "syscall"

// soReusePort is SO_REUSEPORT.
//
// The BSDs and Darwin define it as 0x200 in <sys/socket.h>, a different value
// from Linux's 15 — which is exactly why it is declared per-platform rather
// than once. Hand-defining and citing it is the ADR 0018 discipline, adopted
// because golang.org/x/sys is banned SDK-wide.
const soReusePort int = 0x200

// reusePortSupported reports whether this platform can bind several listeners
// to one address.
func reusePortSupported() bool {
	//: the BSD family has SO_REUSEPORT, though Darwin's does not load-balance
	//: as evenly as Linux's; the engine measures rather than assumes.
	return true
}

// setReusePort enables SO_REUSEPORT on a socket about to be bound.
func setReusePort(fd uintptr) error {
	//: must be set before bind, which is why it runs from the Control hook.
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePort, 1)
}
