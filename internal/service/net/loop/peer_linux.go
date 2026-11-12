//go:build linux

package loop

import (
	"syscall"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// peerOf reads the credentials of the peer of the Unix socket fd, as the kernel
// recorded them when the connection was made (SO_PEERCRED). It allocates
// nothing on every architecture but linux/386 (peerCred). A socket of another
// family answers false — the kernel would answer zeros for a TCP one, which
// name nobody — and so does a peer in a process namespace the kernel cannot
// translate (a process id of zero).
func peerOf(fd int) (corenet.LoopPeerValue, bool) {
	dom, err := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_DOMAIN)
	//: only a Unix socket has a peer to name.
	if err != nil || dom != syscall.AF_UNIX {
		//: unknown.
		return corenet.LoopPeerValue{}, false
	}
	u, ok := peerCred(fd)
	//: refused, or no process the kernel can name.
	if !ok || u.Pid <= 0 {
		//: unknown.
		return corenet.LoopPeerValue{}, false
	}
	//: named by the kernel.
	return corenet.LoopPeerValue{UID: u.Uid, GID: u.Gid, PID: uint32(u.Pid), Verified: true}, true
}

// unixListener reports whether any of fds is a Unix listener.
func unixListener(fds []int) bool {
	//: each listener.
	for _, fd := range fds {
		sa, err := syscall.Getsockname(fd)
		//: a Unix socket.
		if _, unix := sa.(*syscall.SockaddrUnix); err == nil && unix {
			//: found.
			return true
		}
	}
	//: none.
	return false
}
