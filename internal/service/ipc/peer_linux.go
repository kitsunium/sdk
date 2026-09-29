//go:build linux

// Package ipc — the peer's credentials on Linux: SO_PEERCRED, what the peer
// had when it connected or listened.
package ipc

import (
	"net"
	"syscall"
)

// peerOf asks the kernel who is at the other end of c: SO_PEERCRED, the
// credentials the peer had when it connected — or listened —, which it cannot
// forge.
func peerOf(c *net.UnixConn) PeerValue {
	raw, err := c.SyscallConn()
	if err != nil {
		return PeerValue{UID: -1, GID: -1}
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || credErr != nil || cred == nil {
		return PeerValue{UID: -1, GID: -1}
	}
	return PeerValue{UID: int(cred.Uid), GID: int(cred.Gid), PID: int(cred.Pid), Verified: true}
}
