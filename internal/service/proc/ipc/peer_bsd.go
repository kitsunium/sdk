//go:build freebsd || dragonfly || netbsd

package ipc

import (
	"encoding/binary"
	"net"
	"syscall"
	"unsafe"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
)

// peerOf asks the kernel who is at the other end of c through
// getsockopt(SOL_LOCAL, peerOption), decoding the platform's struct
// (peerFromBuffer) from a byte buffer at the platform's width — no x/sys.
func peerOf(c *net.UnixConn) coreipc.PeerValue {
	unknown := coreipc.PeerValue{UID: -1, GID: -1}
	b, ok := peerBuffer(c)
	if !ok {
		return unknown
	}
	peer, ok := peerFromBuffer(b, binary.NativeEndian)
	if !ok {
		return unknown
	}
	return peer
}

// peerGroups is the peer's groups as the same struct gives them
// (groupsFromBuffer): FreeBSD's and DragonFly's xucred carries them, NetBSD's
// unpcbid does not (#1092).
func peerGroups(c *net.UnixConn) []int {
	b, ok := peerBuffer(c)
	if !ok {
		return nil
	}
	return groupsFromBuffer(b, binary.NativeEndian)
}

// peerBuffer is what getsockopt(SOL_LOCAL, peerOption) writes for c.
func peerBuffer(c *net.UnixConn) ([]byte, bool) {
	raw, err := c.SyscallConn()
	if err != nil {
		return nil, false
	}
	var buf [peerBufferSize]byte
	size := uint32(len(buf))
	var errno syscall.Errno
	if err := raw.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall6(syscall.SYS_GETSOCKOPT, fd, solLocal, peerOption,
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0)
	}); err != nil || errno != 0 || int(size) > len(buf) {
		return nil, false
	}
	return buf[:size], true
}

// solLocal is SOL_LOCAL (sys/un.h): the level of the local-socket options.
const solLocal uintptr = 0

// peerBufferSize holds the largest struct the option writes.
const peerBufferSize int = 128
