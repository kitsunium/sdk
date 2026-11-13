//go:build linux

package server

import "syscall"

// ipprotoMPTCP is IPPROTO_MPTCP (Linux 5.6, include/uapi/linux/in.h): absent
// from the standard library's syscall package, and the protocol Go's
// net.Listen asks for by default since Go 1.24.
const ipprotoMPTCP int = 262

// multipathOf reports whether sc's socket is Multipath TCP, read back
// (SO_PROTOCOL): what State says, whoever bound it. A socket that cannot be
// read counts as plain.
func multipathOf(sc syscall.Conn) bool {
	//: a socket with no descriptor to read.
	if sc == nil {
		//: plain.
		return false
	}
	raw, err := sc.SyscallConn()
	//: unreadable counts as plain.
	if err != nil {
		//: plain.
		return false
	}
	proto := 0
	//: read, never written.
	if cerr := raw.Control(func(fd uintptr) {
		v, gerr := syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_PROTOCOL)
		//: the kernel's answer, when it gave one.
		if gerr == nil {
			proto = v
		}
	}); cerr != nil {
		//: plain.
		return false
	}
	//: as the kernel holds it.
	return proto == ipprotoMPTCP
}
