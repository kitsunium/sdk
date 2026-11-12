//go:build linux

package server

import (
	stdnet "net"
	"syscall"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// portablePeer reads the credentials of a Unix connection's peer as the kernel
// recorded them when it connected (SO_PEERCRED). It answers the zero value for
// any other connection, and for a peer in a process namespace the kernel cannot
// translate (a process id of zero).
func portablePeer(c stdnet.Conn) corenet.LoopPeerValue {
	uc, ok := c.(*stdnet.UnixConn)
	//: only a Unix socket has a peer to name.
	if !ok {
		//: nobody.
		return corenet.LoopPeerValue{}
	}
	rc, err := uc.SyscallConn()
	//: no raw access.
	if err != nil {
		//: nobody.
		return corenet.LoopPeerValue{}
	}
	var cred *syscall.Ucred
	var gerr error
	cerr := rc.Control(func(fd uintptr) {
		cred, gerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	//: refused, or no process the kernel can name.
	if cerr != nil || gerr != nil || cred.Pid <= 0 {
		//: nobody.
		return corenet.LoopPeerValue{}
	}
	//: named by the kernel.
	return corenet.LoopPeerValue{UID: cred.Uid, GID: cred.Gid, PID: uint32(cred.Pid), Verified: true}
}
