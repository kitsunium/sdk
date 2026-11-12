//go:build !linux && !windows && !freebsd && !dragonfly && !netbsd

package ipc

import (
	"net"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
)

// peerOf says nothing here: darwin's getpeereid and its raw syscalls are
// deprecated, OpenBSD pins its syscalls, illumos and Solaris answer through
// libc's getpeerucred — all out of the standard library's reach (ADR 0148
// §Deferred, narrowed by #1093). The directory is the gate.
func peerOf(_ *net.UnixConn) coreipc.PeerValue { return coreipc.PeerValue{UID: -1, GID: -1} }

// peerGroups reads nothing here, as peerOf does.
func peerGroups(_ *net.UnixConn) []int { return nil }
