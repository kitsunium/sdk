//go:build linux

package ipc

import (
	"net"
	"syscall"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
)

// peerOf asks the kernel who is at the other end of c: SO_PEERCRED, the
// credentials the peer had when it connected — or listened —, which it cannot
// forge.
func peerOf(c *net.UnixConn) coreipc.PeerValue {
	raw, err := c.SyscallConn()
	if err != nil {
		return coreipc.PeerValue{UID: -1, GID: -1}
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || credErr != nil || cred == nil {
		return coreipc.PeerValue{UID: -1, GID: -1}
	}
	return coreipc.PeerValue{UID: int(cred.Uid), GID: int(cred.Gid), PID: int(cred.Pid), Verified: true}
}
