//go:build linux

package server

import "syscall"

// soReusePort is SO_REUSEPORT.
//
// It is absent from the Go standard library's syscall package, so the value is
// hand-defined and cited here — the discipline ADR 0018 established for ABI
// constants, adopted because golang.org/x/sys is banned SDK-wide. Linux defines
// it in include/asm-generic/socket.h as 15.
const soReusePort int = 0xf

// reuseportDrawJoins is whether a port-0 draw with SO_REUSEPORT set may be
// given a port a reuseport socket of the same user holds, and join its group:
// on Linux it may (udp_lib_lport_inuse, udp_reuseport_add_sock,
// net/ipv4/udp.c), so the first shard of such an address draws without the
// option and is given it once bound (listenShard, #1585).
const reuseportDrawJoins = true

// reusePortSupported reports whether this platform can bind several listeners
// to one address.
func reusePortSupported() bool {
	//: Linux has had SO_REUSEPORT with load balancing since 3.9.
	return true
}

// setReusePort enables SO_REUSEPORT on a socket about to be bound, or on the
// bound first shard of a drawn port (reuseAfterBind).
func setReusePort(fd uintptr) error {
	//: before bind for a socket joining a port; after it, the kernel reads it
	//: when the next socket binds beside it.
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePort, 1)
}
